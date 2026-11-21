package repairtxt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
)

// rateLimited is an lrclib server that answers every search with HTTP 429 and the longest
// Retry-After the client honours, and calls cancel once the answer is sent.
func rateLimited(t *testing.T, cancel func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
		w.(http.Flusher).Flush()
		cancel()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunStopsDuringALyricsLookup(t *testing.T) {
	cfg, paths, _ := twoDamaged(t)
	// "s�rprise" is not in the corpus, so the song asks for lyrics.
	damaged := songText("#TITLE:Eins\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n", "s"+R+"rprise")
	if err := os.WriteFile(paths[0], []byte(damaged), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := filepath.Join(t.TempDir(), "cache")
	c := NewLRCLib(cache, fsops.New(false))
	c.BaseURL, c.Delay = rateLimited(t, cancel).URL, 0
	done := make(chan error, 1)
	go func() { done <- Run(cfg, fsops.New(false), Options{Ctx: ctx, Lyrics: c}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop while the lyrics client waited for a retry")
	}
	if !unchanged(t, paths[0], damaged) {
		t.Error("a run stopped during the lyrics lookup wrote a txt")
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Errorf("a cancelled lookup was cached: %v", entries)
	}
}

func TestLRCLibCancelled(t *testing.T) {
	lookup := func(t *testing.T, ctx context.Context, c *LRCLib) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := c.Lyrics(ctx, "Dr"+R+"mhus", "Vill Ha Dig")
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Lyrics = %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Lyrics did not return after the cancel")
		}
		if entries, _ := os.ReadDir(c.CacheDir); len(entries) != 0 {
			t.Errorf("a cancelled lookup was cached: %v", entries)
		}
	}
	newClient := func(t *testing.T, url string) *LRCLib {
		c := NewLRCLib(filepath.Join(t.TempDir(), "cache"), fsops.New(false))
		c.BaseURL, c.Delay = url, 0
		return c
	}
	t.Run("waiting for a retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		lookup(t, ctx, newClient(t, rateLimited(t, cancel).URL))
	})
	t.Run("waiting for an answer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			select { // the client's 30 s timeout would end it otherwise
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer srv.Close()
		defer close(release)
		lookup(t, ctx, newClient(t, srv.URL))
	})
	t.Run("waiting for the delay", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := newClient(t, "http://127.0.0.1:1")
		c.Delay, c.last = time.Hour, time.Now()
		lookup(t, ctx, c)
	})
}
