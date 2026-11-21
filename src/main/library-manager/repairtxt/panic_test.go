package repairtxt

import (
	"os"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
)

// A panic while planning one txt is counted as a failure; that txt is reported unrepaired
// and left as it is, and the other txt is still repaired.
func TestRunPlanPanic(t *testing.T) {
	cfg, paths, orig := twoDamaged(t)
	old := planSong
	t.Cleanup(func() { planSong = old })
	planSong = func(p *planner, in songInput) *filePlan {
		if in.folder == "A - Eins" {
			panic("planner bug")
		}
		return old(p, in)
	}
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 1 {
		t.Errorf("Failed = %d, want 1 for the panic", st.Failed)
	}
	if !unchanged(t, paths[0], orig[0]) {
		t.Error("the txt whose planning panicked was changed")
	}
	if unchanged(t, paths[1], orig[1]) {
		t.Error("the other txt was not repaired")
	}
	rep, ok := readReport(t, cfg)
	if !ok || len(rep.Files) != 2 {
		t.Fatalf("report files = %+v", rep.Files)
	}
	if f := rep.Files[0]; f.Written || f.Damaged != 1 || f.Fixed != 0 || f.Left != 1 {
		t.Errorf("panicked txt in the report: %+v", f)
	}
	if f := rep.Files[1]; !f.Written || f.Fixed != 1 {
		t.Errorf("other txt in the report: %+v", f)
	}
}

// A panic while planning a txt again with fetched lyrics keeps its first plan.
func TestRunPlanPanicWithLyricsKeepsFirstPlan(t *testing.T) {
	cfg, paths, orig := twoDamaged(t)
	// "s�rprise" is in no corpus, so the txt asks for lyrics; "f�r" is repaired anyway.
	orig[0] = songText("#TITLE:Eins\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n", "f"+R+"r dich", "s"+R+"rprise")
	if err := os.WriteFile(paths[0], []byte(orig[0]), 0o644); err != nil {
		t.Fatal(err)
	}
	old := planSong
	t.Cleanup(func() { planSong = old })
	planSong = func(p *planner, in songInput) *filePlan {
		if len(in.refs) > 0 {
			panic("planner bug")
		}
		return old(p, in)
	}
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{Lyrics: fakeLyrics{"A - Eins": "Sürprise für dich"}}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 1 {
		t.Errorf("Failed = %d, want 1 for the panic", st.Failed)
	}
	rep, ok := readReport(t, cfg)
	if !ok || len(rep.Files) != 2 {
		t.Fatalf("report files = %+v", rep.Files)
	}
	if f := rep.Files[0]; !f.Written || f.Damaged != 2 || f.Fixed != 1 || f.Left != 1 {
		t.Errorf("want the first plan (für repaired, s�rprise left): %+v", f)
	}
}
