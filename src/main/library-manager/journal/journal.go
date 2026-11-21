// Package journal remembers media files that were already processed (or found not worth
// processing). It is needed for file formats that cannot carry a marker tag, such as raw
// AAC or AVI; fixvideos records only those, fixaudio every file it normalizes.
//
// Files are identified by a content fingerprint instead of their path, so entries stay
// valid when songs are renamed or moved (e.g. when the library is reorganized).
package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/nkreimeyer/library-manager/fsops"
)

// chunk is the number of bytes hashed at the start and at the end of a file.
const chunk = 64 << 10

// Journal is a set of (kind, fingerprint) entries stored as a text file. It is safe for
// concurrent use.
type Journal struct {
	path    string
	mu      sync.Mutex
	entries map[string]bool // "kind\tfingerprint"
	dirty   bool
}

// Open loads the journal at path. A missing file is an empty journal.
func Open(path string) (*Journal, error) {
	j := &Journal{path: path, entries: map[string]bool{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			j.entries[line] = true
		}
	}
	return j, sc.Err()
}

// Fingerprint identifies a file's content by its size and a SHA-256 of its first and last
// 64 KiB, which is fast even for large videos.
func Fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, chunk); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// Hash the last chunk too. For files up to 2*chunk this is the rest after the first
	// chunk (so every byte is covered and nothing is hashed twice); the reader is already
	// positioned there.
	if st.Size() > chunk {
		if st.Size() > 2*chunk {
			if _, err := f.Seek(-chunk, io.SeekEnd); err != nil {
				return "", err
			}
		}
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d-%s", st.Size(), hex.EncodeToString(h.Sum(nil))[:32]), nil
}

// Has reports whether the file at path was recorded under kind. Unreadable files are not.
func (j *Journal) Has(kind, path string) bool {
	fp, err := Fingerprint(path)
	if err != nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.entries[kind+"\t"+fp]
}

// Add records the current content of the file at path under kind.
func (j *Journal) Add(kind, path string) error {
	fp, err := Fingerprint(path)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if key := kind + "\t" + fp; !j.entries[key] {
		j.entries[key] = true
		j.dirty = true
	}
	return nil
}

// AddAndSave records the file like Add and immediately persists the journal, so an
// interrupted run does not forget files it already processed (which would re-encode them,
// and back them up, a second time).
func (j *Journal) AddAndSave(kind, path string, ops *fsops.Ops) error {
	if err := j.Add(kind, path); err != nil {
		return err
	}
	return j.Save(ops)
}

// Save writes the journal if it changed. It lives in the output dir, so it is written
// through ops.WriteReport.
func (j *Journal) Save(ops *fsops.Ops) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.dirty {
		return nil
	}
	lines := make([]string, 0, len(j.entries))
	for e := range j.entries {
		lines = append(lines, e)
	}
	sort.Strings(lines)
	data := "# library-manager media journal: <kind> TAB <size>-<hash>; delete a line to reprocess a file\n" +
		strings.Join(lines, "\n") + "\n"
	if err := ops.WriteReport(j.path, []byte(data)); err != nil {
		return err
	}
	j.dirty = false
	return nil
}
