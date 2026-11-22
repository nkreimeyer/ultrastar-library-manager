package webui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/organize"
)

func newStepRun(t *testing.T, tidy *tidyOptions, dry bool) (*stepRun, string) {
	t.Helper()
	lib, out := dedupeLibrary(t)
	cfg, err := organize.NewConfig(organize.ModeDedupe, nil, lib, out, dry)
	if err != nil {
		t.Fatal(err)
	}
	return &stepRun{
		tidy: tidy, cfg: cfg, ops: fsops.New(dry), ctx: context.Background(),
		c: jobControl{r: newRunner(t), j: &job{}}, songs: organize.NewOptions(organize.ModeDedupe, false, false, false),
	}, lib
}

// In a real run a failing media step is logged and counted, the steps after it still run,
// and the run ends with "N step(s) did not finish" and one detail line per failed step.
func TestStepRunMediaStepFailureDoesNotStopTheRest(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: the media steps cannot run
	r, _ := newStepRun(t, &tidyOptions{UTF8: true, Video: true, Audio: true}, false)
	var steps []stepResult
	var err error
	logged := testutil.ErrorLogs(t, func() { steps, err = r.run() })

	var de *detailedError
	if !errors.As(err, &de) || de.msg != "2 step(s) did not finish" {
		t.Fatalf("err = %v, want a detailedError \"2 step(s) did not finish\"", err)
	}
	if len(de.details) != 2 || !strings.HasPrefix(de.details[0], stepVideo+": ") || !strings.HasPrefix(de.details[1], stepAudio+": ") {
		t.Errorf("details = %q", de.details)
	}
	var names []string
	for _, st := range steps {
		names = append(names, st.Step+":"+map[bool]string{true: "ok", false: "failed"}[st.Error == ""])
	}
	if got := strings.Join(names, ","); got != "text:ok,video:failed,audio:failed" {
		t.Errorf("steps = %s", got)
	}
	for _, step := range []string{stepVideo, stepAudio} {
		if !testutil.ContainsPrefix(logged, step+" failed: ") {
			t.Errorf("no error logged for the %s step: %v", step, logged)
		}
	}
}

// In a dry run a failing step ends the run at once: its error names the step, and the
// steps after it do not run (a plan with a missing part would look complete).
func TestStepRunDryRunStepFailureEndsTheRun(t *testing.T) {
	testutil.SkipIfRoot(t)
	r, lib := newStepRun(t, &tidyOptions{UTF8: true, Leftovers: true, Dedupe: true}, true)
	testutil.ChmodUnreadable(t, lib) // the text step cannot list the library
	steps, err := r.run()
	if err == nil || !strings.HasPrefix(err.Error(), stepText+": ") {
		t.Fatalf("err = %v, want one starting with %q", err, stepText+": ")
	}
	var de *detailedError
	if errors.As(err, &de) {
		t.Errorf("a dry-run failure is a plain error, got %+v", de)
	}
	if len(steps) != 1 || steps[0].Step != stepText {
		t.Errorf("steps = %+v, want only the failed text step", steps)
	}
}

// A stop between steps ends the run with the context's error and the results so far.
func TestStepRunStopBetweenSteps(t *testing.T) {
	r, _ := newStepRun(t, &tidyOptions{UTF8: true, Leftovers: true}, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.ctx = ctx
	steps, err := r.run()
	if !errors.Is(err, context.Canceled) || len(steps) != 0 {
		t.Errorf("steps %+v, err %v", steps, err)
	}
}
