package organize

import (
	"fmt"

	"github.com/nkreimeyer/library-manager/fixvideos"
)

// Quality holds what the keeper choice compares between working versions of one song.
// See chooseKeeper for the order of the criteria.
type Quality struct {
	// SongWarnings counts the warnings about the txt, the timing or the audio source
	// (check.SongWarnings); warnings about video, cover, background or stems are not
	// counted, they show up in the criteria below.
	SongWarnings int
	// HasVideo: a readable video is referenced (check.MediaSummary.HasVideo).
	HasVideo    bool
	VideoHeight int
	// VideoBitrate of that video in bit/s; 0 = unknown.
	VideoBitrate int64
	// AudioBitrate of the audio source in bit/s; 0 = unknown.
	AudioBitrate            int64
	HasCover, HasBackground bool
	HasVersion              bool // the txt has a #VERSION tag
}

// bitrateTolerance: audio bitrates within this fraction of the highest bitrate of the
// group count as equal, so 320 and 300 kbit/s (or VBR files of the same preset) do not
// decide anything.
const bitrateTolerance = 0.10

// chooseKeeper picks the best of the working versions cs of one song and returns its
// index and, for every other version, the reason it loses (from the loser's point of
// view, e.g. "no video"; "" for the keeper). The criteria are applied in this order as
// filters over the whole group: at each step only the versions with the best value stay,
// and the reason of a dropped version names the criterion that dropped it:
//
//  1. with priority: the lowest tier (earliest -s flag);
//  2. the fewest song warnings (txt, timing, audio source);
//  3. a readable video, if any version has one;
//  4. a video bitrate at or below fixvideos.BitrateThreshold (unknown counts as below):
//     a higher one only bloats the library until fixvideos re-encodes it;
//  5. the highest video height, counted up to fixvideos.MaxHeight (fixvideos downscales
//     taller videos, so 2160p is no better than 1080p);
//  6. an audio bitrate within 10 % of the highest one (0 = unknown loses against a
//     known one);
//  7. a cover; 8. a background; 9. #VERSION in the txt;
//  10. tie: the earliest source (the order of -s, flattened), then the first relative path.
//
// Filtering the whole group (instead of comparing pairs) keeps the choice transitive
// with the bitrate tolerance: the keeper does not depend on the order of cs, and every
// reason is what actually decided. Two different candidates always differ at least in
// their source or relative path, so exactly one keeper is left.
func chooseKeeper(cs []*Candidate, priority bool) (int, []string) {
	reasons := make([]string, len(cs))
	alive := make([]int, len(cs))
	for i := range alive {
		alive[i] = i
	}
	// keep drops the versions for which ok is false, with their reason.
	keep := func(ok func(*Candidate) bool, reason func(*Candidate) string) {
		var next []int
		for _, i := range alive {
			if ok(cs[i]) {
				next = append(next, i)
			} else {
				reasons[i] = reason(cs[i])
			}
		}
		alive = next
	}
	// best returns the minimum (or, with highest, the maximum) of v over the alive ones.
	best := func(v func(*Candidate) int64, highest bool) int64 {
		b := v(cs[alive[0]])
		for _, i := range alive[1:] {
			if x := v(cs[i]); (highest && x > b) || (!highest && x < b) {
				b = x
			}
		}
		return b
	}
	flag := func(f bool) int64 {
		if f {
			return 1
		}
		return 0
	}

	if priority {
		tier := best(func(c *Candidate) int64 { return int64(c.Tier) }, false)
		keep(func(c *Candidate) bool { return int64(c.Tier) == tier },
			func(c *Candidate) string { return fmt.Sprintf("lower priority tier (-s #%d vs #%d)", c.Tier+1, tier+1) })
	}
	warn := best(func(c *Candidate) int64 { return int64(c.Quality.SongWarnings) }, false)
	keep(func(c *Candidate) bool { return int64(c.Quality.SongWarnings) == warn },
		func(c *Candidate) string {
			return fmt.Sprintf("more song warnings (%d vs %d)", c.Quality.SongWarnings, warn)
		})
	if best(func(c *Candidate) int64 { return flag(c.Quality.HasVideo) }, true) == 1 {
		keep(func(c *Candidate) bool { return c.Quality.HasVideo }, func(*Candidate) string { return "no video" })
	}
	if best(func(c *Candidate) int64 { return flag(!videoBitrateTooHigh(c)) }, true) == 1 {
		keep(func(c *Candidate) bool { return !videoBitrateTooHigh(c) },
			func(c *Candidate) string {
				return fmt.Sprintf("video bitrate too high (%d kbit/s, above the %d kbit/s of fixvideos)",
					c.Quality.VideoBitrate/1000, fixvideos.BitrateThreshold/1000)
			})
	}
	height := best(func(c *Candidate) int64 { return int64(cappedHeight(c)) }, true)
	keep(func(c *Candidate) bool { return int64(cappedHeight(c)) == height },
		func(c *Candidate) string {
			return fmt.Sprintf("lower video height (%dp vs %dp)", c.Quality.VideoHeight, height)
		})
	if rate := best(func(c *Candidate) int64 { return c.Quality.AudioBitrate }, true); rate > 0 {
		keep(func(c *Candidate) bool { return bitrateClose(c.Quality.AudioBitrate, rate) },
			func(c *Candidate) string {
				if c.Quality.AudioBitrate == 0 {
					return fmt.Sprintf("unknown audio bitrate (vs %d kbit/s)", rate/1000)
				}
				return fmt.Sprintf("lower audio bitrate (%d vs %d kbit/s)", c.Quality.AudioBitrate/1000, rate/1000)
			})
	}
	for _, f := range []struct {
		has    func(*Candidate) bool
		reason string
	}{
		{func(c *Candidate) bool { return c.Quality.HasCover }, "no cover"},
		{func(c *Candidate) bool { return c.Quality.HasBackground }, "no background"},
		{func(c *Candidate) bool { return c.Quality.HasVersion }, "no #VERSION"},
	} {
		if best(func(c *Candidate) int64 { return flag(f.has(c)) }, true) == 1 {
			keep(f.has, func(*Candidate) string { return f.reason })
		}
	}
	src := best(func(c *Candidate) int64 { return int64(c.Source) }, false)
	keep(func(c *Candidate) bool { return int64(c.Source) == src },
		func(*Candidate) string { return "same quality; the version from the earlier source is kept" })
	first := cs[alive[0]].Rel
	for _, i := range alive[1:] {
		first = min(first, cs[i].Rel)
	}
	keep(func(c *Candidate) bool { return c.Rel == first },
		func(*Candidate) string { return "same quality; the version with the first path is kept" })
	return alive[0], reasons
}

// videoBitrateTooHigh: the video is above the bitrate fixvideos re-encodes.
func videoBitrateTooHigh(c *Candidate) bool {
	return c.Quality.VideoBitrate > fixvideos.BitrateThreshold
}

// cappedHeight is the video height as far as it counts: up to fixvideos.MaxHeight.
func cappedHeight(c *Candidate) int {
	return min(c.Quality.VideoHeight, fixvideos.MaxHeight)
}

// better reports whether a is the better of the two versions a and b (chooseKeeper over
// the pair), and the reason the loser loses.
func better(a, b *Candidate, priority bool) (bool, string) {
	k, reasons := chooseKeeper([]*Candidate{a, b}, priority)
	return k == 0, reasons[1-k]
}

// bitrateClose reports whether the known bitrate b counts as equal to the highest
// bitrate hi of the group: within bitrateTolerance of it.
func bitrateClose(b, hi int64) bool {
	return b > 0 && float64(hi-b) <= bitrateTolerance*float64(hi)
}
