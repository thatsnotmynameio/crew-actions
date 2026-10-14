package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/proc"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend/local"
)

// Covers AE7.
func TestAFailingStepFailsTheAttemptAndSkipsTheRest(t *testing.T) {
	e := newFakeEngine(t, work(1, "one", "two", "three"))
	b := &fakeBackend{steps: map[string]fakeStep{
		"two": func(context.Context, io.Writer) (int, error) { return 1, nil },
	}}
	run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"update 2 running", "update 2 finished failure 1",
		"update 3 skipped",
		"done failure",
		"next",
	)
	b.wantDestroyed(t, 1)
}

func TestEveryStepSucceeds(t *testing.T) {
	e := newFakeEngine(t, work(1, "one", "two"))
	run(t, e, &fakeBackend{})
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"update 2 running", "update 2 finished success 0",
		"done success",
		"next",
	)
}

func TestAnAcceptConflictDropsTheWorkflow(t *testing.T) {
	e := newFakeEngine(t, work(1, "one"), work(2, "two"))
	e.acceptErrs = map[int64]error{1: fmt.Errorf("taken: %w", model.ErrConflict)}
	b := &fakeBackend{}
	stderr := run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"next", "accept 2",
		"update 1 running", "update 1 finished success 0",
		"done success",
		"next",
	)
	if b.setups != 1 {
		t.Errorf("Setup was called %d times, want once, for the second workflow", b.setups)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing for a conflict", stderr)
	}
}

func TestAFailedAcceptIsReportedAndTheRunnerAsksAgain(t *testing.T) {
	e := newFakeEngine(t, work(1, "one"))
	e.acceptErrs = map[int64]error{1: errors.New("the engine broke")}
	b := &fakeBackend{}
	stderr := run(t, e, b)
	e.want(t, "next", "accept 1", "next")
	if b.setups != 0 {
		t.Errorf("Setup was called %d times, want none", b.setups)
	}
	if !strings.Contains(stderr, "the engine broke") {
		t.Errorf("stderr = %q, want the accept's error", stderr)
	}
}

func TestNextRetriesWhileTheEngineIsUnavailable(t *testing.T) {
	unavailable := fmt.Errorf("connection refused: %w", model.ErrUnavailable)
	e := newFakeEngine(t,
		nextAnswer{err: unavailable},
		nextAnswer{err: unavailable},
		nextAnswer{resp: &protocol.NextResponse{}},
		work(1, "one"),
	)
	start := time.Now()
	run(t, e, &fakeBackend{})
	e.want(t,
		"next", "next", "next", "next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"done success",
		"next",
	)
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("two retries took %v, want a second between tries", elapsed)
	}
}

func TestAFailedNextThatIsNotUnavailableEndsTheRunner(t *testing.T) {
	broken := errors.New("the engine broke")
	e := newFakeEngine(t, nextAnswer{err: broken})
	r := &Runner{Engine: e, Backend: &fakeBackend{}, Stderr: io.Discard}
	if err := r.Run(context.Background()); !errors.Is(err, broken) {
		t.Errorf("Run = %v, want the next's error", err)
	}
	e.want(t, "next")
}

func TestNextWaitsLongerThanTheEnginesWindow(t *testing.T) {
	e := newFakeEngine(t, work(1, "one"))
	run(t, e, &fakeBackend{})
	deadline, ok := e.firstNextDeadline()
	if !ok {
		t.Fatal("next was called with no deadline")
	}
	if left := time.Until(deadline); left <= 25*time.Second || left > time.Minute {
		t.Errorf("next's deadline is in %v, want a margin over the engine's 25-second window", left)
	}
}

func TestCancellingTheRunnerStopsTheStepWithoutDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	b := &fakeBackend{steps: map[string]fakeStep{
		"long": func(ctx context.Context, _ io.Writer) (int, error) {
			cancel()
			<-ctx.Done()
			close(stopped)
			return -1, fmt.Errorf("the step was stopped: %w", ctx.Err())
		},
	}}
	e := newFakeEngine(t, work(1, "long", "after"))
	r := &Runner{Engine: e, Backend: b, Stderr: io.Discard}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("the step was not stopped")
	}
	e.want(t, "next", "accept 1", "update 1 running")
	b.wantDestroyed(t, 1)
}

func TestAWorkspaceThatCannotBeSetUpFailsTheAttempt(t *testing.T) {
	e := newFakeEngine(t, work(1, "one", "two"))
	b := &fakeBackend{setupErr: errors.New("no space left")}
	stderr := run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"update 1 skipped", "update 2 skipped",
		"done failure",
		"next",
	)
	if !strings.Contains(stderr, "no space left") {
		t.Errorf("stderr = %q, want the setup's error", stderr)
	}
}

func TestAWorkspaceThatCannotBeRemovedKeepsTheOutcome(t *testing.T) {
	e := newFakeEngine(t, work(1, "one"))
	b := &fakeBackend{destroyErr: errors.New("busy")}
	stderr := run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"done success",
		"next",
	)
	if !strings.Contains(stderr, "busy") {
		t.Errorf("stderr = %q, want the removal's error", stderr)
	}
}

func TestAFailedDoneIsReportedAndTheRunnerAsksAgain(t *testing.T) {
	e := newFakeEngine(t, work(1, "one"), work(2, "two"))
	e.doneErr = errors.New("the engine broke")
	stderr := run(t, e, &fakeBackend{})
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"done success",
		"next", "accept 2",
		"update 1 running", "update 1 finished success 0",
		"done success",
		"next",
	)
	if !strings.Contains(stderr, "the engine broke") {
		t.Errorf("stderr = %q, want the done's error", stderr)
	}
}

func TestAFailedUpdateCancelsTheAttempt(t *testing.T) {
	e := newFakeEngine(t, work(1, "one", "two"))
	e.updateErr = errors.New("the engine broke")
	var stepCtxErr error
	b := &fakeBackend{steps: map[string]fakeStep{
		"one": func(ctx context.Context, _ io.Writer) (int, error) {
			stepCtxErr = ctx.Err()
			return 0, nil
		},
	}}
	stderr := run(t, e, b)
	e.want(t, "next", "accept 1", "update 1 running", "next")
	if !errors.Is(stepCtxErr, context.Canceled) {
		t.Errorf("the step's context ended with %v, want the attempt cancelled", stepCtxErr)
	}
	if !strings.Contains(stderr, "the engine broke") {
		t.Errorf("stderr = %q, want the update's error", stderr)
	}
}

func TestTheLocalBackendShipsTheStepsOutput(t *testing.T) {
	e := newFakeEngine(t, nextAnswer{resp: &protocol.NextResponse{
		WorkflowId: 1, Version: 2,
		Workflow: &protocol.Workflow{Name: "print", Steps: []*protocol.Step{
			{Name: "hello", Commands: []string{"echo hello", "echo oops >&2", "exit 3"}},
		}},
	}})
	run(t, e, &local.Backend{Group: new(proc.Group), BaseDir: t.TempDir()})
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished failure 3",
		"done failure",
		"next",
	)
	if got := e.output(1); got != "hello\noops\n" {
		t.Errorf("the step's log = %q, want what it printed", got)
	}
}

// run runs a runner against e and b until e has no more work, and returns
// what the runner wrote to its stderr.
func run(t *testing.T, e *fakeEngine, b backend.Backend) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.idle = cancel
	stderr := new(bytes.Buffer)
	r := &Runner{Engine: e, Backend: b, Stderr: stderr}
	ended := make(chan error, 1)
	go func() { ended <- r.Run(ctx) }()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
	return stderr.String()
}

// nextAnswer is what the fake engine answers to one next.
type nextAnswer struct {
	resp *protocol.NextResponse
	err  error
}

// work is a next's answer handing out workflow id, whose steps are named
// names, each running one command, its name, which the fake backend looks
// up.
func work(id int64, names ...string) nextAnswer {
	wf := &protocol.Workflow{Name: "test"}
	for _, n := range names {
		wf.Steps = append(wf.Steps, &protocol.Step{Name: n, Commands: []string{n}})
	}
	return nextAnswer{resp: &protocol.NextResponse{WorkflowId: id, Version: id * 10, Workflow: wf}}
}

// fakeEngine is a protocol.Engine in memory. It answers next from its
// script, and once the script is spent calls idle and waits for the
// caller's context to end. It records every call but log as one of calls,
// and every log apart.
type fakeEngine struct {
	t          *testing.T
	script     []nextAnswer
	idle       func()
	acceptErrs map[int64]error
	updateErr  error
	doneErr    error
	// logGate, when set, holds every log until it is closed.
	logGate chan struct{}
	// logSize, when set, is the size a log answers.
	logSize func(req *protocol.LogRequest) int64

	mu            sync.Mutex
	calls         []string
	logs          []logCall
	nextDeadlines []time.Time
	attempt       int64
	versions      map[int64]int64
}

// logCall is one log the fake engine received, and how many other calls it
// had received before it.
type logCall struct {
	req    *protocol.LogRequest
	before int
}

func newFakeEngine(t *testing.T, script ...nextAnswer) *fakeEngine {
	t.Helper()
	return &fakeEngine{t: t, script: script, versions: map[int64]int64{}}
}

func (e *fakeEngine) Next(ctx context.Context, _ *protocol.NextRequest) (*protocol.NextResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, "next")
	if d, ok := ctx.Deadline(); ok {
		e.nextDeadlines = append(e.nextDeadlines, d)
	}
	if len(e.script) == 0 {
		e.mu.Unlock()
		if e.idle != nil {
			e.idle()
		}
		<-ctx.Done()
		return nil, fmt.Errorf("next: %w", ctx.Err())
	}
	a := e.script[0]
	e.script = e.script[1:]
	if a.resp != nil {
		e.versions[a.resp.GetWorkflowId()] = a.resp.GetVersion()
	}
	e.mu.Unlock()
	return a.resp, a.err
}

func (e *fakeEngine) Accept(_ context.Context, req *protocol.AcceptRequest) (*protocol.AcceptResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, fmt.Sprintf("accept %d", req.GetWorkflowId()))
	if v := e.versions[req.GetWorkflowId()]; v != req.GetVersion() {
		e.t.Errorf("accept of workflow %d at version %d, want the version next returned, %d",
			req.GetWorkflowId(), req.GetVersion(), v)
	}
	if err := e.acceptErrs[req.GetWorkflowId()]; err != nil {
		return nil, err
	}
	e.attempt = 100 + req.GetWorkflowId()
	return &protocol.AcceptResponse{AttemptId: e.attempt}, nil
}

func (e *fakeEngine) Update(_ context.Context, req *protocol.UpdateRequest) (*protocol.UpdateResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkAttempt(req.GetAttemptId())
	call := fmt.Sprintf("update %d %s", req.GetPosition(), state(req.GetState()))
	if req.GetState() == protocol.StepState_STEP_STATE_FINISHED {
		status := "none"
		if req.ExitStatus != nil {
			status = strconv.Itoa(int(req.GetExitStatus()))
		}
		call += fmt.Sprintf(" %s %s", outcome(req.GetOutcome()), status)
	}
	e.calls = append(e.calls, call)
	if e.updateErr != nil {
		return nil, e.updateErr
	}
	return &protocol.UpdateResponse{}, nil
}

func (e *fakeEngine) Log(ctx context.Context, req *protocol.LogRequest) (*protocol.LogResponse, error) {
	e.mu.Lock()
	e.checkAttempt(req.GetAttemptId())
	e.logs = append(e.logs, logCall{req: req, before: len(e.calls)})
	e.mu.Unlock()
	if e.logGate != nil {
		select {
		case <-e.logGate:
		case <-ctx.Done():
			return nil, fmt.Errorf("log: %w", ctx.Err())
		}
	}
	size := req.GetOffset() + int64(len(req.GetData()))
	if e.logSize != nil {
		size = e.logSize(req)
	}
	return &protocol.LogResponse{Size: size}, nil
}

func (e *fakeEngine) Done(_ context.Context, req *protocol.DoneRequest) (*protocol.DoneResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkAttempt(req.GetAttemptId())
	e.calls = append(e.calls, "done "+outcome(req.GetOutcome()))
	if e.doneErr != nil {
		return nil, e.doneErr
	}
	return &protocol.DoneResponse{}, nil
}

// checkAttempt fails the test unless id is the attempt the last accept
// started.
func (e *fakeEngine) checkAttempt(id int64) {
	if id != e.attempt {
		e.t.Errorf("a call names attempt %d, want %d", id, e.attempt)
	}
}

// want fails the test unless the engine received calls, in order, logs
// aside.
func (e *fakeEngine) want(t *testing.T, calls ...string) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !reflect.DeepEqual(e.calls, calls) {
		t.Errorf("the engine received %q, want %q", e.calls, calls)
	}
}

// stepLogs returns the logs the engine received for the step at position.
func (e *fakeEngine) stepLogs(position int32) []logCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	var logs []logCall
	for _, l := range e.logs {
		if l.req.GetPosition() == position {
			logs = append(logs, l)
		}
	}
	return logs
}

// output is the data of the logs the engine received for the step at
// position, joined.
func (e *fakeEngine) output(position int32) string {
	var b strings.Builder
	for _, l := range e.stepLogs(position) {
		b.Write(l.req.GetData())
	}
	return b.String()
}

// firstNextDeadline is the deadline of the first next's context.
func (e *fakeEngine) firstNextDeadline() (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.nextDeadlines) == 0 {
		return time.Time{}, false
	}
	return e.nextDeadlines[0], true
}

func state(s protocol.StepState) string {
	st, err := protocol.StepStateToModel(s)
	if err != nil {
		return s.String()
	}
	return string(st)
}

func outcome(o protocol.Outcome) string {
	out, err := protocol.OutcomeToModel(o)
	if err != nil {
		return o.String()
	}
	return string(out)
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
