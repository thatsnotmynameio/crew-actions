package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/proc"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend/local"
	"github.com/thatsnotmynameio/crew-actions/internal/spec"
)

func TestEveryStepSucceeds(t *testing.T) {
	b := &fakeBackend{}
	obs := &recorder{}
	outcome, err := Run(context.Background(), workflow("one", "two", "three"), b, obs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome != Success {
		t.Errorf("outcome = %q, want %q", outcome, Success)
	}
	obs.want(t,
		"started 0", "finished 0: status 0",
		"started 1", "finished 1: status 0",
		"started 2", "finished 2: status 0",
	)
	b.wantDestroyed(t, 1)
}

// Covers AE7 at the executor's level.
func TestAFailingStepFailsTheWorkflowAndSkipsTheRest(t *testing.T) {
	b := &fakeBackend{steps: map[string]fakeStep{
		"two": func(context.Context, io.Writer) (int, error) { return 3, nil },
	}}
	obs := &recorder{}
	outcome, err := Run(context.Background(), workflow("one", "two", "three"), b, obs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome != Failure {
		t.Errorf("outcome = %q, want %q", outcome, Failure)
	}
	obs.want(t,
		"started 0", "finished 0: status 0",
		"started 1", "finished 1: status 3",
		"skipped 2",
	)
	b.wantDestroyed(t, 1)
}

func TestAStepThatCannotStartFailsTheWorkflow(t *testing.T) {
	startErr := errors.New("no shell")
	b := &fakeBackend{steps: map[string]fakeStep{
		"two": func(context.Context, io.Writer) (int, error) { return -1, startErr },
	}}
	obs := &recorder{}
	outcome, err := Run(context.Background(), workflow("one", "two", "three"), b, obs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome != Failure {
		t.Errorf("outcome = %q, want %q", outcome, Failure)
	}
	obs.want(t,
		"started 0", "finished 0: status 0",
		"started 1", "finished 1: error",
		"skipped 2",
	)
	r := obs.results[1]
	if !errors.Is(r.Err, startErr) || r.ExitStatus != -1 || r.Stopped {
		t.Errorf("step 1's result = %+v, want the start error, no exit status and not stopped", r)
	}
	b.wantDestroyed(t, 1)
}

func TestACancelledContextRunsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &fakeBackend{}
	obs := &recorder{}
	outcome, err := Run(ctx, workflow("one", "two", "three"), b, obs)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want the context's cancellation", err)
	}
	if outcome != "" {
		t.Errorf("outcome = %q, want none", outcome)
	}
	obs.want(t, "skipped 0", "skipped 1", "skipped 2")
	if b.setups != 0 {
		t.Errorf("Setup was called %d times, want none", b.setups)
	}
	b.wantDestroyed(t, 0)
}

func TestAContextCancelledDuringAStepStopsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &fakeBackend{steps: map[string]fakeStep{
		"two": func(ctx context.Context, _ io.Writer) (int, error) {
			cancel()
			<-ctx.Done()
			return -1, fmt.Errorf("the step was stopped: %w", ctx.Err())
		},
	}}
	obs := &recorder{}
	outcome, err := Run(ctx, workflow("one", "two", "three"), b, obs)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want the context's cancellation", err)
	}
	if outcome != "" {
		t.Errorf("outcome = %q, want none", outcome)
	}
	obs.want(t,
		"started 0", "finished 0: status 0",
		"started 1", "finished 1: stopped",
		"skipped 2",
	)
	b.wantDestroyed(t, 1)
}

func TestAContextCancelledBetweenStepsSkipsTheRest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &fakeBackend{steps: map[string]fakeStep{
		"one": func(context.Context, io.Writer) (int, error) {
			cancel()
			return 0, nil
		},
	}}
	obs := &recorder{}
	outcome, err := Run(ctx, workflow("one", "two", "three"), b, obs)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want the context's cancellation", err)
	}
	if outcome != "" {
		t.Errorf("outcome = %q, want none", outcome)
	}
	obs.want(t, "started 0", "finished 0: status 0", "skipped 1", "skipped 2")
	b.wantDestroyed(t, 1)
}

func TestAFailedSetupSkipsEveryStep(t *testing.T) {
	setupErr := errors.New("no space left")
	b := &fakeBackend{setupErr: setupErr}
	obs := &recorder{}
	outcome, err := Run(context.Background(), workflow("one", "two", "three"), b, obs)
	if !errors.Is(err, setupErr) {
		t.Errorf("Run = %v, want the setup error", err)
	}
	if outcome != "" {
		t.Errorf("outcome = %q, want none", outcome)
	}
	obs.want(t, "skipped 0", "skipped 1", "skipped 2")
	b.wantDestroyed(t, 0)
}

func TestAFailedRemovalKeepsTheOutcome(t *testing.T) {
	destroyErr := errors.New("busy")
	b := &fakeBackend{destroyErr: destroyErr}
	obs := &recorder{}
	outcome, err := Run(context.Background(), workflow("one", "two"), b, obs)
	if outcome != Success {
		t.Errorf("outcome = %q, want %q", outcome, Success)
	}
	if !errors.Is(err, destroyErr) {
		t.Errorf("Run = %v, want the removal error", err)
	}
	b.wantDestroyed(t, 1)
}

func TestAPanickingObserverStillDestroysTheWorkspace(t *testing.T) {
	b := &fakeBackend{}
	obs := &recorder{panicOnFinished: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Run did not panic")
			}
		}()
		_, _ = Run(context.Background(), workflow("one", "two"), b, obs)
	}()
	b.wantDestroyed(t, 1)
}

func TestTheLocalBackendRunsStepsInOneFreshDirectory(t *testing.T) {
	base := t.TempDir()
	b := &local.Backend{Group: new(proc.Group), BaseDir: base}
	first := spec.Workflow{Name: "first", Steps: []spec.Step{
		{Name: "make", Commands: []string{"pwd", "touch made"}},
		{Name: "list", Commands: []string{"ls"}},
	}}
	obs := &recorder{}
	if outcome, err := Run(context.Background(), first, b, obs); err != nil || outcome != Success {
		t.Fatalf("Run = %q, %v, want success", outcome, err)
	}
	if got := obs.output(1); got != "made\n" {
		t.Errorf("the second step printed %q, want the file the first made", got)
	}
	wantEmpty(t, base)

	second := spec.Workflow{Name: "second", Steps: []spec.Step{
		{Name: "look", Commands: []string{"pwd", "ls -A"}},
	}}
	again := &recorder{}
	if outcome, err := Run(context.Background(), second, b, again); err != nil || outcome != Success {
		t.Fatalf("second Run = %q, %v, want success", outcome, err)
	}
	firstDir := strings.TrimSpace(obs.output(0))
	if got := strings.TrimSpace(again.output(0)); got == firstDir || strings.Contains(got, "\n") {
		t.Errorf("the second Run printed %q, want a directory other than %q, and empty", got, firstDir)
	}
	wantEmpty(t, base)
}

func TestAFailingWriterNeitherBlocksNorFailsTheStep(t *testing.T) {
	b := &local.Backend{Group: new(proc.Group), BaseDir: t.TempDir()}
	wf := spec.Workflow{Name: "loud", Steps: []spec.Step{
		{Name: "print", Commands: []string{"head -c 200000 /dev/zero"}},
	}}
	writeErr := errors.New("the connection is gone")
	obs := &recorder{writer: &failingWriter{first: writeErr}}
	outcome, err := Run(context.Background(), wf, b, obs)
	if err != nil || outcome != Success {
		t.Fatalf("Run = %q, %v, want success", outcome, err)
	}
	r := obs.results[0]
	if r.Err != nil || r.ExitStatus != 0 {
		t.Errorf("the step's result = %+v, want status 0", r)
	}
	if !errors.Is(r.WriteErr, writeErr) {
		t.Errorf("the step's write error = %v, want the writer's first error", r.WriteErr)
	}
}

// workflow returns a workflow whose steps are named names, each running one
// command, its name, which the fake backend looks up.
func workflow(names ...string) spec.Workflow {
	wf := spec.Workflow{Name: "test"}
	for _, n := range names {
		wf.Steps = append(wf.Steps, spec.Step{Name: n, Commands: []string{n}})
	}
	return wf
}

// wantEmpty fails the test unless dir holds nothing: every workspace under it
// was removed.
func wantEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%s still holds %d entries, want none", dir, len(entries))
	}
}

// fakeStep is what a fake workspace does for a step.
type fakeStep func(ctx context.Context, out io.Writer) (int, error)

// fakeBackend sets up fake workspaces. A step runs the fakeStep its first
// command names, and exits 0 when there is none.
type fakeBackend struct {
	steps      map[string]fakeStep
	setupErr   error
	destroyErr error
	setups     int
	destroys   int
}

func (b *fakeBackend) Setup() (backend.Workspace, error) {
	b.setups++
	if b.setupErr != nil {
		return nil, b.setupErr
	}
	return &fakeWorkspace{backend: b}, nil
}

// wantDestroyed fails the test unless n workspaces were destroyed.
func (b *fakeBackend) wantDestroyed(t *testing.T, n int) {
	t.Helper()
	if b.destroys != n {
		t.Errorf("Destroy was called %d times, want %d", b.destroys, n)
	}
}

type fakeWorkspace struct {
	backend *fakeBackend
}

func (w *fakeWorkspace) Run(ctx context.Context, commands []string, out io.Writer) (int, error) {
	if step, ok := w.backend.steps[commands[0]]; ok {
		return step(ctx, out)
	}
	return 0, nil
}

func (w *fakeWorkspace) Destroy() error {
	w.backend.destroys++
	return w.backend.destroyErr
}

// recorder is an observer that records what it is told. Each step's output
// goes to a buffer of its own, or to writer when it is set.
type recorder struct {
	writer          io.Writer
	panicOnFinished bool
	events          []string
	results         map[int]Result
	outputs         map[int]*bytes.Buffer
}

func (r *recorder) Started(i int) io.Writer {
	r.events = append(r.events, fmt.Sprintf("started %d", i))
	if r.writer != nil {
		return r.writer
	}
	if r.outputs == nil {
		r.outputs = map[int]*bytes.Buffer{}
	}
	r.outputs[i] = new(bytes.Buffer)
	return r.outputs[i]
}

func (r *recorder) Finished(i int, res Result) {
	if r.panicOnFinished {
		panic("the observer failed")
	}
	if r.results == nil {
		r.results = map[int]Result{}
	}
	r.results[i] = res
	var how string
	switch {
	case res.Stopped:
		how = "stopped"
	case res.Err != nil:
		how = "error"
	default:
		how = fmt.Sprintf("status %d", res.ExitStatus)
	}
	r.events = append(r.events, fmt.Sprintf("finished %d: %s", i, how))
}

func (r *recorder) Skipped(i int) {
	r.events = append(r.events, fmt.Sprintf("skipped %d", i))
}

// output is what step i printed.
func (r *recorder) output(i int) string {
	if out, ok := r.outputs[i]; ok {
		return out.String()
	}
	return ""
}

// want fails the test unless the recorder was told events, in order.
func (r *recorder) want(t *testing.T, events ...string) {
	t.Helper()
	if !reflect.DeepEqual(r.events, events) {
		t.Errorf("the observer saw %q, want %q", r.events, events)
	}
}

// failingWriter fails every write: the first with first, the others with an
// error of their own.
type failingWriter struct {
	first  error
	writes int
}

func (w *failingWriter) Write([]byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return 0, w.first
	}
	return 0, errors.New("a later write")
}
