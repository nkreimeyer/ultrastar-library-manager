package organize

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/utils"
)

// The folders of the output dir that hold what the tools set aside, and where they come
// from. Only a move the journal knows has an exact origin; the mirrored layout of
// extra-files/ and invalid/ (<out>/<section>/<path in the library>) gives a likely one.
const (
	SectionRemoved    = "removed"     // remove
	SectionDuplicates = "duplicates"  // organize, dedupe, add: <label>/<path>
	SectionInvalid    = "invalid"     // organize, dedupe, add (<label>/<path>), fixtxt, cleanup, check (<path>)
	SectionExtraFiles = "extra-files" // fixtxt, cleanup: files no song uses
)

// Sections lists the sections in the order they are shown.
var Sections = []string{SectionInvalid, SectionDuplicates, SectionRemoved, SectionExtraFiles}

// Kinds of a SetAsideItem.
const (
	KindSong = "song" // a song folder
	KindFile = "file" // a leftover file
)

// SetAsideItem is one song folder or file in the output dir that can be put back.
type SetAsideItem struct {
	Section string `json:"section"`
	Rel     string `json:"rel"` // below the section folder, with "/"
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"` // "Artist - Title" of a song
	// Origin is where it is put back to (absolute); empty if that is not known.
	Origin string `json:"origin,omitempty"`
	// Exact: Origin is the place the journal says it was moved from; otherwise it is the
	// path below the library that the folder layout suggests.
	Exact bool `json:"exact"`
	// Blocked says why it cannot be put back now (no origin, or something in the way).
	Blocked string `json:"blocked,omitempty"`
}

// RestoreRef names an item of a section.
type RestoreRef struct {
	Section string `json:"section"`
	Rel     string `json:"rel"`
}

const (
	blockedUnknown  = "it is not known where this came from"
	blockedInTheWay = "something is already in its old place"
	blockedOutside  = "its old place is inside the output folder"
	// blockedNoFolder is for an origin outside the library (a source folder of organize
	// or add) whose folder is gone, say an unplugged USB drive: putting it back would
	// create a fresh tree there, on the wrong file system. Inside the library missing
	// folders are created.
	blockedNoFolder = "its old folder %s does not exist any more"
	// notListed: a ref that is not an item ListSetAside shows (a category, a label folder,
	// a file inside a song folder).
	notListed = "it is not an item of the set-aside list (a whole song folder or a leftover file)"
)

// journalOrigins maps every path the journal moved something to (and that was not moved
// back) to the path it came from. Later moves of the same path win.
func journalOrigins(cfg config.OrganizeConfig) (map[string]string, error) {
	runs, err := readJournal(cfg.JournalPath())
	if err != nil {
		return nil, fmt.Errorf("reading the journal: %w", err)
	}
	m := map[string]string{}
	for _, r := range runs {
		if r.Undone {
			continue
		}
		for n, e := range r.Entries {
			if e.Op == opMove && !r.Reverted[n+1] {
				m[filepath.Clean(e.Dst)] = filepath.Clean(e.Src)
			}
		}
	}
	return m, nil
}

// sectionRel cleans a relative path of the UI: it must stay below the section.
func sectionRel(rel string) (string, error) {
	r := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || r == "." || filepath.IsAbs(r) || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	return r, nil
}

// origin returns where the item at <out>/<section>/<rel> is put back to, and whether the
// journal said so. "" = unknown.
func origin(cfg config.OrganizeConfig, journal map[string]string, section, rel string) (string, bool) {
	path := filepath.Join(cfg.OutDir, section, rel)
	if src, ok := journal[path]; ok {
		return src, true
	}
	switch section {
	case SectionExtraFiles, SectionInvalid:
		if cfg.Library == "" {
			return "", false // a guess needs the library (else rel would be relative to the CWD)
		}
		return filepath.Join(cfg.Library, rel), false
	}
	return "", false
}

// blocked returns why origin cannot take the item now ("" = it can).
func blocked(cfg config.OrganizeConfig, ops *fsops.Ops, origin string) string {
	switch {
	case origin == "" || !filepath.IsAbs(origin): // a relative one would be below the CWD
		return blockedUnknown
	case config.IsWithin(origin, cfg.OutDir):
		return blockedOutside
	case ops.Exists(origin):
		return blockedInTheWay
	}
	if parent, gone := goneOriginFolder(cfg.Library, cfg.OutDir, ops, origin); gone {
		return fmt.Sprintf(blockedNoFolder, parent)
	}
	return ""
}

// goneOriginFolder returns the parent of origin, and true if putting something back to
// origin would create a fresh tree outside library ("" = unknown) and outDir: the parent
// is gone (an unplugged drive, an unmounted share), and MkdirAll would recreate it on the
// wrong file system. Restore and Undo refuse such a move.
func goneOriginFolder(library, outDir string, ops *fsops.Ops, origin string) (string, bool) {
	parent := filepath.Dir(origin)
	if (library != "" && config.IsWithin(origin, library)) || config.IsWithin(origin, outDir) || ops.Exists(parent) {
		return "", false
	}
	return parent, true
}

func validSection(section string) bool {
	for _, s := range Sections {
		if s == section {
			return true
		}
	}
	return false
}

// ListSetAside lists what is in the output dir of cfg: the song folders of invalid/,
// duplicates/ and removed/, and the files of extra-files/, each with where it would be
// put back to. It only reads. Unreadable folders are logged and skipped.
func ListSetAside(cfg config.OrganizeConfig, ops *fsops.Ops) ([]SetAsideItem, error) {
	journal, err := journalOrigins(cfg)
	if err != nil {
		return nil, err
	}
	return listSetAside(cfg, ops, journal, Sections, true)
}

// listSetAside lists the items of the given sections. names = read the song tags for
// SetAsideItem.Name (Restore only needs the paths).
func listSetAside(cfg config.OrganizeConfig, ops *fsops.Ops, journal map[string]string, sections []string, names bool) ([]SetAsideItem, error) {
	items := []SetAsideItem{}
	var err error
	for _, section := range sections {
		dir := filepath.Join(cfg.OutDir, section)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		var got []SetAsideItem
		if section == SectionExtraFiles {
			err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					ops.SkipUnreadable(p, err)
					return fs.SkipDir
				}
				if d.IsDir() {
					if p != dir && utils.IsHidden(d.Name()) {
						return fs.SkipDir
					}
					return nil
				}
				if utils.IsHidden(d.Name()) {
					return nil
				}
				rel, _ := filepath.Rel(dir, p)
				got = append(got, SetAsideItem{Section: section, Rel: filepath.ToSlash(rel), Kind: KindFile})
				return nil
			})
		} else {
			got, err = listSongFolders(ops, section, dir, names)
		}
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", dir, err)
		}
		for i := range got {
			it := &got[i]
			it.Origin, it.Exact = origin(cfg, journal, section, filepath.FromSlash(it.Rel))
			it.Blocked = blocked(cfg, ops, it.Origin)
		}
		sort.Slice(got, func(a, b int) bool { return got[a].Rel < got[b].Rel })
		items = append(items, got...)
	}
	return items, nil
}

func listSongFolders(ops *fsops.Ops, section, dir string, names bool) ([]SetAsideItem, error) {
	folders, err := utils.GetAllSongFolders(ops, dir)
	if err != nil {
		return nil, err
	}
	songs := make([]removeSong, len(folders))
	if !names {
		for i, f := range folders {
			songs[i] = noTagsSong(dir, f)
		}
	} else {
		_ = utils.ForEachParallel(context.Background(), folders, utils.Parallel{
			Workers: workers,
			OnPanic: func(i int, _ any) { songs[i] = noTagsSong(dir, folders[i]) },
		}, func(i int, folder string) { songs[i] = readSong(ops, dir, folder) })
	}
	items := make([]SetAsideItem, len(songs))
	for i, s := range songs {
		items[i] = SetAsideItem{Section: section, Rel: s.Rel, Kind: KindSong}
		if !s.NoTags && s.Artist != "" {
			items[i].Name = s.Artist + " - " + s.Title
		}
	}
	return items, nil
}

// RestoreOptions are the optional parts of Restore.
type RestoreOptions struct {
	Ctx      context.Context // stops before the next item; nil = never
	Progress func(done, total int)
}

// Restore moves the named items back to where they came from (see SetAsideItem.Origin).
// A ref that is not an item of ListSetAside (same Section and Rel), an item without a
// known origin, or one whose old place is taken stays and is counted as a failure
// (ops.Fail); nothing is overwritten or merged. The moves form one journal run,
// so the undo of the output folder moves them back again. It returns an error only if the
// output folder cannot be listed, the journal cannot be written or the run was stopped.
func Restore(cfg config.OrganizeConfig, ops *fsops.Ops, refs []RestoreRef, opts RestoreOptions) error {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	journal, err := journalOrigins(cfg)
	if err != nil {
		return err
	}
	listed, err := listedRefs(cfg, ops, journal, refs)
	if err != nil {
		return err
	}
	j := newJournal(ops, cfg.JournalPath(), cfg.Library)
	restored := 0
	for n, ref := range refs {
		if err := ctx.Err(); err != nil {
			log.Warnf("Stopping: no further items are put back")
			return err
		}
		if opts.Progress != nil {
			opts.Progress(n, len(refs))
		}
		rel, err := sectionRel(ref.Rel)
		if err != nil || !validSection(ref.Section) {
			log.Errorf("Restore: %q in %q is not an item of the output folder", ref.Rel, ref.Section)
			ops.Fail()
			continue
		}
		path := filepath.Join(cfg.OutDir, ref.Section, rel)
		if !listed[RestoreRef{ref.Section, filepath.ToSlash(rel)}] {
			log.Errorf("Restore: %s stays: %s", path, notListed)
			ops.Fail()
			continue
		}
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			log.Errorf("Restore: %s is not there any more", path)
			ops.Fail()
			continue
		}
		dst, _ := origin(cfg, journal, ref.Section, rel)
		if why := blocked(cfg, ops, dst); why != "" {
			log.Errorf("Restore: %s stays: %s", path, why)
			ops.Fail()
			continue
		}
		if !cfg.DryRun {
			if err := j.begin(); err != nil {
				return fmt.Errorf("the journal %s cannot be written, so nothing more is put back: %w", cfg.JournalPath(), err)
			}
			for _, d := range missingDirs(filepath.Dir(dst)) {
				if err := j.mkdir(d); err != nil {
					return fmt.Errorf("the journal %s cannot be written, so nothing more is put back: %w", cfg.JournalPath(), err)
				}
			}
		}
		if err := ops.MoveTo(path, dst); err != nil {
			log.Errorf("Restore: %v", err) // counted by ops
			continue
		}
		restored++
		log.Infof("Put back %s", dst)
		if err := j.moved(path, dst); err != nil {
			return fmt.Errorf("the move to %s is not in the journal %s, so nothing more is put back: %w", dst, cfg.JournalPath(), err)
		}
		// The folders it leaves empty in the output folder are tidied away.
		root := filepath.Join(cfg.OutDir, ref.Section)
		for d := filepath.Dir(path); d != root && config.IsWithin(d, root); d = filepath.Dir(d) {
			if removed, err := ops.RemoveEmptyDir(d); err != nil || !removed {
				break
			}
		}
	}
	if opts.Progress != nil {
		opts.Progress(len(refs), len(refs))
	}
	log.Infof("Restore: %d of %d item(s) put back", restored, len(refs))
	return nil
}

// listedRefs lists the sections refs name once (the same discovery as ListSetAside, so
// what Restore accepts cannot drift from what the list shows) and returns its items.
func listedRefs(cfg config.OrganizeConfig, ops *fsops.Ops, journal map[string]string, refs []RestoreRef) (map[RestoreRef]bool, error) {
	var sections []string
	for _, s := range Sections {
		for _, ref := range refs {
			if ref.Section == s {
				sections = append(sections, s)
				break
			}
		}
	}
	items, err := listSetAside(cfg, ops, journal, sections, false)
	if err != nil {
		return nil, err
	}
	listed := make(map[RestoreRef]bool, len(items))
	for _, it := range items {
		listed[RestoreRef{it.Section, it.Rel}] = true
	}
	return listed, nil
}
