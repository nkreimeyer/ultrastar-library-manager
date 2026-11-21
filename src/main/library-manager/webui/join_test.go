package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestOrganizeJoinFlow(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock"), "Queen", "Rock", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock (Live)"), "Queen", "Rock (Live)", nil)
	writeSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", nil)

	v := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	// All fixture songs share one chart, so every pair is listed; the two Queen versions are
	// the one pair that is also a possible duplicate.
	if v.Summary.Duplicates != 0 || len(v.Similar) != 3 || len(v.Joined) != 0 {
		t.Fatalf("before the join: %+v similar %+v", v.Summary, v.Similar)
	}
	var pair [2]string
	for _, sp := range v.Similar {
		if sp.Possible {
			pair = [2]string{v.Songs[sp.A].Ref, v.Songs[sp.B].Ref}
		}
	}
	if pair[0] == "" {
		t.Fatal("no possible pair")
	}

	var e apiError
	for _, bad := range [][][2]string{{{pair[0], "library:nope"}}, {{pair[0], pair[0]}}} {
		if got := s.do("POST", "/api/organize/join", map[string]any{"analysis": v.ID, "join": bad}, &e); got != http.StatusBadRequest {
			t.Errorf("bad pair %v: %d", bad, got)
		}
	}
	var v2 organizeView
	if got := s.do("POST", "/api/organize/join", map[string]any{"analysis": v.ID, "join": [][2]string{pair}}, &v2); got != http.StatusOK {
		t.Fatalf("join: %d", got)
	}
	if v2.Summary.Duplicates != 1 || len(v2.Similar) != 1 || len(v2.Joined) != 1 || v2.Joined[0] != pair {
		t.Fatalf("after the join: %+v similar %+v joined %v", v2.Summary, v2.Similar, v2.Joined)
	}
	// An exclusion keeps the join.
	var v3 organizeView
	s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{}}, &v3)
	if v3.Summary.Duplicates != 1 || len(v3.Joined) != 1 {
		t.Errorf("after exclude: %+v", v3.Summary)
	}

	// The real run applies the join.
	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 {
		t.Fatalf("apply job: %+v", j)
	}
	moved := 0
	for _, name := range []string{"Queen - Rock", "Queen - Rock (Live)"} {
		if _, err := os.Stat(filepath.Join(out, "duplicates", "library", "Q", name)); err == nil {
			moved++
		}
	}
	if moved != 1 {
		t.Errorf("%d version(s) set aside, want 1", moved)
	}

	// Keeping them apart again, on a fresh check, lists the pair again.
	v4 := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	if len(v4.Joined) != 0 {
		t.Errorf("a new check kept the join: %v", v4.Joined)
	}
}
