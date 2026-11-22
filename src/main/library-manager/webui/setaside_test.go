package webui

import (
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/organize"
)

func TestSetAsideBrowseAndRestore(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	before := testutil.Tree(t, lib)

	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Fatalf("remove: %+v", j)
	}
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "Q", "Queen - Rock", "old.jpg"), "img")

	var cv configView
	if got := s.do("POST", "/api/setaside/validate", folderRequest{Library: lib, Out: out}, &cv); got != http.StatusOK || cv.OutDir != out {
		t.Fatalf("validate: %d %+v", got, cv)
	}
	var e apiError

	var list struct {
		Items []organize.SetAsideItem `json:"items"`
	}
	if got := s.do("POST", "/api/setaside/list", folderRequest{Library: lib, Out: out}, &list); got != http.StatusOK || len(list.Items) != 3 {
		t.Fatalf("list: %d %+v", got, list.Items)
	}
	var refs []organize.RestoreRef
	for _, it := range list.Items {
		if it.Blocked != "" {
			t.Errorf("blocked: %+v", it)
		}
		if it.Section == organize.SectionRemoved {
			refs = append(refs, organize.RestoreRef{Section: it.Section, Rel: it.Rel})
		}
	}
	if got := s.do("POST", "/api/setaside/restore", map[string]any{"library": lib, "out": out, "items": []organize.RestoreRef{}}, &e); got != http.StatusBadRequest {
		t.Errorf("nothing chosen: %d", got)
	}
	// A folder that is not an item of the list (here a folder of leftover files whose
	// mirrored place is free) is refused, not moved wholesale.
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "N", "Nobody - X", "a.jpg"), "img")
	unlisted := map[string]any{"library": lib, "out": out, "items": []organize.RestoreRef{{Section: organize.SectionExtraFiles, Rel: "N"}}}
	if got := s.do("POST", "/api/setaside/restore", unlisted, &j); got != http.StatusAccepted {
		t.Fatalf("restore unlisted: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 0 || j.Stats.Failed != 1 {
		t.Errorf("restore unlisted: %+v", j)
	}
	if testutil.Exists(filepath.Join(lib, "N")) {
		t.Error("an unlisted folder was put back")
	}
	if err := os.RemoveAll(filepath.Join(out, "extra-files", "N")); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"library": lib, "out": out, "items": refs}
	if got := s.do("POST", "/api/setaside/restore", body, &j); got != http.StatusAccepted {
		t.Fatalf("restore: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 || j.Stats.Failed != 0 {
		t.Fatalf("restore job: %+v", j)
	}
	delete(before, "") // the library is as before, plus nothing
	if got := testutil.Tree(t, lib); !maps.Equal(got, before) {
		t.Errorf("library differs after the restore")
	}
	// Only the leftover file is still set aside.
	s.do("POST", "/api/setaside/list", folderRequest{Library: lib, Out: out}, &list)
	if len(list.Items) != 1 || list.Items[0].Section != organize.SectionExtraFiles {
		t.Errorf("after restore: %+v", list.Items)
	}
}

// A restore request with dryRun only plans: nothing moves (a real move would not be
// journaled, so undo could not take it back).
func TestSetAsideRestoreDryRunMovesNothing(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)

	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Fatalf("remove: %+v", j)
	}
	var list struct {
		Items []organize.SetAsideItem `json:"items"`
	}
	s.do("POST", "/api/setaside/list", folderRequest{Library: lib, Out: out}, &list)
	var refs []organize.RestoreRef
	for _, it := range list.Items {
		refs = append(refs, organize.RestoreRef{Section: it.Section, Rel: it.Rel})
	}
	if len(refs) != 2 {
		t.Fatalf("set aside: %+v", list.Items)
	}
	libBefore, outBefore := testutil.Tree(t, lib), testutil.Tree(t, out)
	body := map[string]any{"library": lib, "out": out, "dryRun": true, "items": refs}
	if got := s.do("POST", "/api/setaside/restore", body, &j); got != http.StatusAccepted {
		t.Fatalf("restore: %d", got)
	}
	if j = s.wait(); j.State != stateDone || !j.DryRun || j.Stats.Moved != 2 || j.Stats.Failed != 0 {
		t.Fatalf("restore job: %+v", j)
	}
	if got := testutil.Tree(t, lib); !maps.Equal(got, libBefore) {
		t.Errorf("a dry-run restore changed the library")
	}
	if got := testutil.Tree(t, out); !maps.Equal(got, outBefore) {
		t.Errorf("a dry-run restore changed the output folder")
	}
}
