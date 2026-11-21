package repairtxt

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/fsops"
)

// LyricsSource looks up the plain lyrics of a song. Artist and title may still contain
// U+FFFD. It returns "" without an error if it does not know the song. Once ctx is done, a
// lookup that still needs the network stops with an error that wraps ctx.Err() (an
// answer already at hand, such as a cached one, may still be returned). An error means
// there is no answer: found lyrics come with a nil error, even if keeping them for later
// (a cache) failed.
type LyricsSource interface {
	Lyrics(ctx context.Context, artist, title string) (string, error)
}

// DefaultLRCLibURL is the API of lrclib.net, a free lyrics database that needs no key.
const DefaultLRCLibURL = "https://lrclib.net/api"

// LRCLib looks lyrics up at lrclib.net. Answers (also "not found") are cached as JSON
// files in CacheDir, so a second run sends no request for the same song. A cache file that
// cannot be written is logged and counted by Ops; the answer is still returned.
type LRCLib struct {
	BaseURL  string
	Client   *http.Client
	CacheDir string
	Ops      *fsops.Ops // writes the cache files (ops.WriteReport)
	// Delay is the pause between two requests.
	Delay time.Duration
	// RetryWait is the first pause after an HTTP 429/503 answer without Retry-After; it
	// doubles with every retry (4 attempts in all).
	RetryWait time.Duration
	last      time.Time
}

// NewLRCLib returns a client for lrclib.net that caches in cacheDir.
func NewLRCLib(cacheDir string, ops *fsops.Ops) *LRCLib {
	return &LRCLib{
		BaseURL: DefaultLRCLibURL, Client: &http.Client{Timeout: 30 * time.Second},
		CacheDir: cacheDir, Ops: ops, Delay: 300 * time.Millisecond, RetryWait: 5 * time.Second,
	}
}

type lrclibRecord struct {
	TrackName   string `json:"trackName"`
	ArtistName  string `json:"artistName"`
	PlainLyrics string `json:"plainLyrics"`
}

// lyricsCacheVersion is the version of the lookup that wrote a cache entry. A "not found"
// entry of an older version is looked up again (version 2 added the fallback queries).
const lyricsCacheVersion = 2

type cacheEntry struct {
	Version int    `json:"version,omitempty"`
	Artist  string `json:"artist"`
	Title   string `json:"title"`
	Found   bool   `json:"found"`
	Lyrics  string `json:"lyrics,omitempty"`
}

// matchKey lower-cases s and keeps letters, digits and U+FFFD.
func matchKey(s string) []rune {
	var out []rune
	for _, r := range norm.NFC.String(s) {
		if r == replChar || unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToLower(r))
		}
	}
	return out
}

// baseRune is r without its diacritics (é = e).
func baseRune(r rune) rune {
	for _, b := range norm.NFD.String(string(r)) {
		return b
	}
	return r
}

// looseEqual compares two match keys. Diacritics do not matter ("Desole" = "Désolé"), and
// a U+FFFD on either side matches any non-ASCII rune (lrclib's own metadata is sometimes
// damaged as well).
func looseEqual(a, b []rune) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] == b[i] || baseRune(a[i]) == baseRune(b[i]) {
			continue
		}
		if (a[i] == replChar && b[i] >= 0x80) || (b[i] == replChar && a[i] >= 0x80) {
			continue
		}
		return false
	}
	return true
}

// looseContains reports whether b contains a or a contains b (loosely compared), for
// artists written with or without a featured artist.
func looseContains(a, b []rune) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(a) < 3 {
		return looseEqual(a, b)
	}
	for i := 0; i+len(a) <= len(b); i++ {
		if looseEqual(a, b[i:i+len(a)]) {
			return true
		}
	}
	return false
}

// searchQuery is the words of artist and title without U+FFFD: a damaged word is split at
// it and its parts of two or more letters are kept ("Dr�mhus" = "Dr mhus").
func searchQuery(artist, title string) string {
	var parts []string
	for _, w := range strings.Fields(artist + " " + title) {
		for _, p := range strings.Split(w, string(replChar)) {
			if len([]rune(p)) >= 2 {
				parts = append(parts, p)
			}
		}
	}
	return strings.Join(parts, " ")
}

var (
	qualifierRe = regexp.MustCompile(`\s*(\([^)]*\)|\[[^\]]*\])`)
	// collabRe splits an artist into its collaborators.
	collabRe = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring|&|\+|und|and|et|con|with|och|vs\.?|x)\s+|,`)
)

// cleanTitle is title without its bracketed qualifiers ("Herz an Herz (Duett)",
// "Sexy - Live [VIDEO]" = "Sexy - Live").
func cleanTitle(title string) string {
	return strings.TrimSpace(qualifierRe.ReplaceAllString(title, ""))
}

// firstArtist is the first collaborator of artist ("Shakira feat. Carlinhos Brown" = "Shakira").
func firstArtist(artist string) string {
	return strings.TrimSpace(collabRe.Split(artist, 2)[0])
}

// titleMatches compares two titles loosely, also without their bracketed qualifiers
// ("Deine Liebe (Live)" = "Deine Liebe", "Das Schaferlied" = "Das Schäferlied (Radio Version)").
func titleMatches(theirs, ours string) bool {
	return looseEqual(matchKey(theirs), matchKey(ours)) ||
		looseEqual(matchKey(cleanTitle(theirs)), matchKey(cleanTitle(ours)))
}

// pickRecord returns the lyrics of the first record whose title matches and whose artist
// matches or contains (or is contained in) ours or our first collaborator.
func pickRecord(recs []lrclibRecord, artist, title string) string {
	ta, tf := matchKey(artist), matchKey(firstArtist(artist))
	for _, r := range recs {
		if r.PlainLyrics == "" || !titleMatches(r.TrackName, title) {
			continue
		}
		if ra := matchKey(r.ArtistName); looseContains(ra, ta) || looseContains(ra, tf) {
			return r.PlainLyrics
		}
	}
	return ""
}

// searchQueries are the queries tried in order: artist and title, then the first
// collaborator and the title without qualifiers, then that title alone (lrclib's search
// misses a song if a query word is missing from its record).
func searchQueries(artist, title string) []string {
	var qs []string
	for _, q := range []string{
		searchQuery(artist, title),
		searchQuery(firstArtist(artist), cleanTitle(title)),
		searchQuery("", cleanTitle(title)),
	} {
		if q != "" && !slices.Contains(qs, q) {
			qs = append(qs, q)
		}
	}
	return qs
}

func (c *LRCLib) cachePath(artist, title string) string {
	h := sha1.Sum([]byte(strings.ToLower(artist) + "\x00" + strings.ToLower(title)))
	return filepath.Join(c.CacheDir, hex.EncodeToString(h[:])+".json")
}

// sleep waits d or until ctx is done (a Retry-After may be minutes).
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// search sends the query, waiting Delay since the previous request, and retries on a
// rate limit (HTTP 429) or an unavailable server (503).
func (c *LRCLib) search(ctx context.Context, q string) ([]byte, error) {
	wait := c.RetryWait
	for attempt := 1; ; attempt++ {
		if err := sleep(ctx, c.Delay-time.Since(c.last)); err != nil {
			return nil, err
		}
		c.last = time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/search?q="+url.QueryEscape(q), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "lyrics-lookup/1.0")
		resp, err := c.Client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < 4 {
			d := wait
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 300 {
				d = time.Duration(s) * time.Second
			}
			if err := sleep(ctx, d); err != nil {
				return nil, err
			}
			wait *= 2
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("lyrics search %q: HTTP %s", q, resp.Status)
		}
		return body, err
	}
}

// Lyrics implements LyricsSource. A failed or cancelled search is not cached.
func (c *LRCLib) Lyrics(ctx context.Context, artist, title string) (string, error) {
	cp := c.cachePath(artist, title)
	if data, err := os.ReadFile(cp); err == nil {
		var e cacheEntry
		if json.Unmarshal(data, &e) == nil && (e.Found || e.Version >= lyricsCacheVersion) {
			return e.Lyrics, nil
		}
	}
	qs := searchQueries(artist, title)
	if len(qs) == 0 {
		return "", nil
	}
	lyrics := ""
	for _, q := range qs {
		body, err := c.search(ctx, q)
		if err != nil {
			return "", err
		}
		var recs []lrclibRecord
		if err := json.Unmarshal(body, &recs); err != nil {
			return "", fmt.Errorf("lyrics search %q: %w", q, err)
		}
		if lyrics = pickRecord(recs, artist, title); lyrics != "" {
			break
		}
	}
	e := cacheEntry{Version: lyricsCacheVersion, Artist: artist, Title: title, Found: lyrics != "", Lyrics: lyrics}
	data, err := json.MarshalIndent(e, "", "  ")
	if err == nil && c.Ops != nil {
		// The answer is still good; WriteReport counts the failure.
		if werr := c.Ops.WriteReport(cp, data); werr != nil {
			log.Warnf("caching lyrics of %s - %s: %v", artist, title, werr)
		}
	}
	return lyrics, nil
}
