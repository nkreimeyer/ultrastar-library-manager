package organize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/songtxt"
)

// minContentNotes is the number of notes a song needs for a content hash: with fewer, two
// different songs (test files, stubs) could have the same notes by chance.
const minContentNotes = 10

// ContentHash fingerprints what is sung in a song: #BPM (the last occurrence, as players
// read it) and its old-format BPM changes, and every note with its type, absolute start
// beat, length, pitch, syllable (NFC) and singer. Two txts with the same hash are the same
// chart, whatever their tags say. It returns "" if the song has fewer than
// minContentNotes notes or no valid #BPM.
//
// Ignored, so that copies that differ only there still match: all other tags (#GAP too:
// the same chart synced to another audio file is still the same song), line breaks (they
// only lay out the lyrics), the order of the note lines (notes are sorted), RELATIVE mode
// (beats are absolute) and the encoding and line endings of the file. A song with a
// single singer marker (a solo file starting with "P1") counts as having none. Notes,
// timing and lyrics must match exactly: a single changed syllable or beat gives another hash.
func ContentHash(s *songtxt.Song) string {
	if s == nil || len(s.Notes) < minContentNotes {
		return ""
	}
	bpm, err := songtxt.ParseNumber(tagValue(s, "BPM"))
	if err != nil || bpm <= 0 {
		return ""
	}
	solo := !s.IsDuet()
	notes := make([]songtxt.Note, len(s.Notes))
	for i, n := range s.Notes {
		if solo {
			n.Player = 0
		}
		n.Text = norm.NFC.String(n.Text)
		n.Line = 0
		notes[i] = n
	}
	sort.Slice(notes, func(a, b int) bool {
		x, y := notes[a], notes[b]
		switch {
		case x.Player != y.Player:
			return x.Player < y.Player
		case x.Start != y.Start:
			return x.Start < y.Start
		case x.Length != y.Length:
			return x.Length < y.Length
		case x.Pitch != y.Pitch:
			return x.Pitch < y.Pitch
		case x.Type != y.Type:
			return x.Type < y.Type
		}
		return x.Text < y.Text
	})
	changes := append([]songtxt.BPMChange(nil), s.BPMChanges...)
	sort.Slice(changes, func(a, b int) bool {
		if changes[a].Beat != changes[b].Beat {
			return changes[a].Beat < changes[b].Beat
		}
		return changes[a].BPM < changes[b].BPM
	})

	h := sha256.New()
	fmt.Fprintf(h, "bpm %s\n", strconv.FormatFloat(bpm, 'g', -1, 64))
	for _, c := range changes {
		fmt.Fprintf(h, "B %d %s\n", c.Beat, strconv.FormatFloat(c.BPM, 'g', -1, 64))
	}
	for _, n := range notes {
		// The syllable is quoted, so spaces and line-like content cannot shift fields.
		fmt.Fprintf(h, "%d %c %d %d %d %s\n", n.Player, n.Type, n.Start, n.Length, n.Pitch, strconv.Quote(n.Text))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// contentKey is the key MakePlan merges groups by: the content hash, separately for duets
// and solo versions (a title-only duet never merges with the solo version, like SongKey).
// "" if the candidate has no hash.
func contentKey(c *Candidate) string {
	if c.ContentHash == "" {
		return ""
	}
	if c.Duet {
		return c.ContentHash + "|duet"
	}
	return c.ContentHash
}

// unionFind joins song keys into groups; the root of a set is its smallest key, so the
// merged group sorts where its first song key does.
type unionFind map[string]string

func (u unionFind) find(k string) string {
	for {
		p, ok := u[k]
		if !ok || p == k {
			return k
		}
		u[k] = u[p] // path halving
		k = p
	}
}

func (u unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	u[rb] = ra
	if _, ok := u[ra]; !ok {
		u[ra] = ra
	}
}

// joinedByContent is appended to the reason of a duplicate whose song key differs from
// the keeper's: it is only in the group because of its notes.
const joinedByContent = "; the tags differ, but the notes, timing and lyrics are identical to another version of this song"

// joinedBySimilar: the version is in the group because of the similarity check.
const joinedBySimilar = "; the tags differ, but the chart (timing and melody, within 30 ms) and at least 90 % of the lyrics match another version of this song"

// joinedByUser: the version is in the group because the reviewer said so.
const joinedByUser = "; the tags differ, but you chose to treat it as the same song as another version"

// joinedViaGroup: the version has the tags of a version that was joined by its notes.
const joinedViaGroup = "; the tags differ, but a version with the same tags matches this song by its notes"

// joinedViaGroupSimilar: the version has the tags of a version that the similarity check
// joined.
const joinedViaGroupSimilar = "; the tags differ, but a version with the same tags matches this song by its chart and lyrics"

// joinedViaGroupUser: the version has the tags of a version the reviewer joined.
const joinedViaGroupUser = "; the tags differ, but you chose to treat a version with the same tags as the same song"
