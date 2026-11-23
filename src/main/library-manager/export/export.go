// Package export writes the song list of a library as a CSV file: one row per song with
// artist, title, year, genre, BPM, language, length and whether it is a duet.
//
// Everything comes from the song txts, so no ffmpeg is needed and the library is only
// read. The CSV follows RFC 4180: the separator is a comma, and a field that holds a
// comma, a double quote or a line break is wrapped in double quotes, with every double
// quote inside doubled (`Crosby, Stills & Nash` becomes `"Crosby, Stills & Nash"`).
package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// FileName is the name of the CSV in the output folder.
const FileName = "song_list.csv"

// Header is the first row of the CSV. The length is in whole seconds: a time such as
// "3:45" would be read by a spreadsheet as three hours and 45 minutes.
var Header = []string{"Artist", "Title", "Year", "Genre", "BPM", "Language", "Length (s)", "Duet"}

// bom makes Excel read the file as UTF-8 (without it, it takes the system's ANSI codepage
// and breaks every umlaut).
const bom = "\xEF\xBB\xBF"

// Song is one row of the list.
type Song struct {
	Artist, Title string
	Year, Genre   string
	// BPM is the #BPM tag as a number with a dot as decimal separator (the tag may use a
	// comma), or as written if it is no number. It is the value in the file, which
	// UltraStar counts in quarter beats: the tempo of the music is a quarter of it.
	BPM      string
	Language string
	// LengthSec is the time of the end of the last note in whole seconds (#GAP and the
	// tempo applied), the length of the sung part of the song; the audio is usually a few
	// seconds longer. HasLength is false if the txt has no notes or no usable #BPM.
	LengthSec int
	HasLength bool
	Duet      bool // notes of two singers (songtxt.Song.IsDuet)
	// Folder is the song folder; not in the CSV, it makes the order of equal songs stable.
	Folder string
}

// Options of Collect and Run.
type Options struct {
	Ctx      context.Context // stops reading further songs; nil = never
	Progress func(done, total int)
}

// FromTxt builds the row of a song from its parsed txt. A missing #ARTIST or #TITLE is
// taken from an obvious "Artist - Title" folder name, as the check and fixtxt do.
func FromTxt(folder string, s *songtxt.Song) Song {
	tag := func(key string) string {
		v, _ := s.Tag(key)
		return strings.TrimSpace(v)
	}
	r := Song{
		Folder: folder,
		Artist: tag("ARTIST"), Title: tag("TITLE"),
		Year: tag("YEAR"), Genre: tag("GENRE"), Language: tag("LANGUAGE"),
		Duet: s.IsDuet(),
	}
	if fill := utils.TagsFromFolderName(filepath.Base(folder), r.Artist, r.Title); fill.FillArtist || fill.FillTitle {
		if fill.FillArtist {
			r.Artist = fill.Artist
		}
		if fill.FillTitle {
			r.Title = fill.Title
		}
	}
	if raw, ok := s.Tag("BPM"); ok {
		if f, err := songtxt.ParseNumber(raw); err == nil {
			r.BPM = strconv.FormatFloat(f, 'f', -1, 64)
		} else {
			r.BPM = strings.TrimSpace(raw)
		}
	}
	if beat, ok := s.LastNoteEndBeat(); ok {
		if ms, err := s.BeatToMs(float64(beat)); err == nil && ms >= 0 {
			r.LengthSec, r.HasLength = int(ms/1000+0.5), true
		}
	}
	return r
}

// Collect reads the song txt of every song folder of library. A folder whose txt cannot
// be read is logged and counted as a failure in ops; a txt that cannot be decoded
// unambiguously is exported as well as it can be read and logged. The rows are sorted by
// artist and title (case and accents ignored). It returns ctx.Err() if it was stopped.
func Collect(ops *fsops.Ops, library string, opts Options) ([]Song, error) {
	folders, err := utils.GetAllSongFolders(ops, library)
	if err != nil {
		return nil, err
	}
	var (
		mu    sync.Mutex
		songs = make([]Song, 0, len(folders))
	)
	err = utils.ForEachParallel(opts.Ctx, folders, utils.Parallel{
		Workers:  min(16, runtime.NumCPU()),
		Progress: opts.Progress,
		OnPanic:  func(i int, _ any) { log.Errorf("Could not read the song in %s", folders[i]); ops.Fail() },
	}, func(_ int, folder string) {
		txt, err := utils.FindSongTxt(ops, folder)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) { // a folder the tools treat as a song but without a song txt is no song
				ops.SkipUnreadable(folder, err)
			}
			return
		}
		data, err := ops.ReadFile(txt)
		if err != nil {
			ops.SkipUnreadable(folder, err)
			return
		}
		d := utils.DecodeTxt(data)
		if d.Problem != "" {
			log.Warnf("%s: the song text may be read with the wrong encoding (%s); its row can show wrong letters", txt, d.Problem)
		}
		s, _ := songtxt.Parse(d.Text)
		row := FromTxt(folder, s)
		mu.Lock()
		songs = append(songs, row)
		mu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	sortSongs(songs)
	return songs, nil
}

func sortSongs(songs []Song) {
	type key struct{ artist, title string }
	keys := make(map[string]key, len(songs))
	for _, s := range songs {
		keys[s.Folder] = key{utils.FoldString(s.Artist), utils.FoldString(s.Title)}
	}
	sort.Slice(songs, func(i, j int) bool {
		a, b := keys[songs[i].Folder], keys[songs[j].Folder]
		if a.artist != b.artist {
			return a.artist < b.artist
		}
		if a.title != b.title {
			return a.title < b.title
		}
		return songs[i].Folder < songs[j].Folder
	})
}

// Record is the CSV row of a song, in the order of Header.
func (s Song) Record() []string {
	length := ""
	if s.HasLength {
		length = strconv.Itoa(s.LengthSec)
	}
	duet := "no"
	if s.Duet {
		duet = "yes"
	}
	return []string{s.Artist, s.Title, s.Year, s.Genre, s.BPM, s.Language, length, duet}
}

// WriteCSV writes the header and one row per song. withBOM starts the file with a UTF-8
// byte order mark, which Excel needs to show umlauts right (other readers may show it as
// part of the first header).
func WriteCSV(w io.Writer, songs []Song, withBOM bool) error {
	if withBOM {
		if _, err := io.WriteString(w, bom); err != nil {
			return err
		}
	}
	cw := csv.NewWriter(w) // comma, RFC 4180 quoting
	if err := cw.Write(Header); err != nil {
		return err
	}
	for _, s := range songs {
		if err := cw.Write(s.Record()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Run exports the songs of library to the CSV file at path (written atomically through
// ops, also when ops is a dry run: it is a report, the library is only read). It returns
// the number of songs written. A stopped run writes nothing.
func Run(ops *fsops.Ops, library, path string, withBOM bool, opts Options) (int, error) {
	songs, err := Collect(ops, library, opts)
	if err != nil {
		return 0, err
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, songs, withBOM); err != nil {
		return 0, fmt.Errorf("encoding the song list: %w", err)
	}
	if err := ops.WriteReport(path, buf.Bytes()); err != nil {
		return 0, err
	}
	return len(songs), nil
}
