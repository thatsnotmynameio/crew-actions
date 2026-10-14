package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/store/logs"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/postgrestest"
)

// testWindow is the hand-out's window in the tests, short so that a Next
// with no work returns soon.
const testWindow = 2 * time.Second

// repo is the repository every test build belongs to.
var repo = model.Repository{Host: "github.com", Organization: "thatsnotmynameio", Name: "crew-actions"}

// workflow is the compiled workflow every test build stores as its
// definition.
var workflow = &protocol.Workflow{
	Name: "test",
	Steps: []*protocol.Step{
		{Name: "checkout", Commands: []string{"git status"}},
		{Name: "build", Commands: []string{"go build ./...", "go vet ./..."}},
		{Name: "test", Commands: []string{"go test ./..."}},
	},
}

// fixture is an engine on a database and a log directory of its own.
type fixture struct {
	engine *Engine
	store  *postgres.Store
	logs   *logs.Dir
	// stop stops the engine.
	stop context.CancelFunc
}

// newFixture returns an engine on a migrated database of its own, with its
// logs in a temporary directory and the test window. It skips the test
// without a database.
func newFixture(t *testing.T) fixture {
	t.Helper()
	s, err := postgres.Open(t.Context(), postgrestest.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(s.Close)
	id, err := s.DatabaseID(t.Context())
	if err != nil {
		t.Fatalf("DatabaseID() error = %v", err)
	}
	dir := logs.New(t.TempDir(), id)
	stop, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := New(stop, s, dir)
	e.window = testWindow
	return fixture{engine: e, store: s, logs: dir, stop: cancel}
}

// createBuild creates a build of repo whose one workflow is workflow, as a
// submit does, and returns its id.
func (f fixture) createBuild(t *testing.T) model.BuildID {
	t.Helper()
	definition, err := proto.Marshal(workflow)
	if err != nil {
		t.Fatalf("encode the workflow: %v", err)
	}
	id, err := f.store.CreateBuild(t.Context(), repo, []model.NewWorkflow{{Name: "test", Definition: definition}})
	if err != nil {
		t.Fatalf("CreateBuild() error = %v", err)
	}
	return id
}

// build returns the snapshot of the build id.
func (f fixture) build(t *testing.T, id model.BuildID) model.Build {
	t.Helper()
	b, err := f.store.GetBuild(t.Context(), id)
	if err != nil {
		t.Fatalf("GetBuild() error = %v", err)
	}
	return b
}

// next calls Next and fails the test on an error.
func (f fixture) next(t *testing.T) *protocol.NextResponse {
	t.Helper()
	resp, err := f.engine.Runners().Next(t.Context(), &protocol.NextRequest{})
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	return resp
}

// accepted creates a build, hands its workflow out and accepts it, and
// returns the build's id and the attempt's id.
func (f fixture) accepted(t *testing.T) (model.BuildID, model.AttemptID) {
	t.Helper()
	id := f.createBuild(t)
	got := f.next(t)
	resp, err := f.engine.Runners().Accept(t.Context(), &protocol.AcceptRequest{
		WorkflowId: got.GetWorkflowId(), Version: got.GetVersion(),
	})
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	return id, model.AttemptID(resp.GetAttemptId())
}

func TestNextHandsOutTheOldestPendingWorkflow(t *testing.T) {
	f := newFixture(t)
	first, second := f.createBuild(t), f.createBuild(t)
	got := f.next(t)
	w := f.build(t, first).Workflows[0]
	if got.GetWorkflowId() != int64(w.ID) || got.GetVersion() != 2 {
		t.Errorf("Next() = workflow %d at version %d, want %d at version 2",
			got.GetWorkflowId(), got.GetVersion(), w.ID)
	}
	if !proto.Equal(got.GetWorkflow(), workflow) {
		t.Errorf("Next() workflow = %v, want %v", got.GetWorkflow(), workflow)
	}
	if w.State != model.WorkflowAssigned || w.Version != 2 {
		t.Errorf("handed-out workflow = %s at version %d, want assigned at version 2", w.State, w.Version)
	}
	if other := f.build(t, second).Workflows[0]; other.State != model.WorkflowPending {
		t.Errorf("newer workflow = %s, want pending", other.State)
	}
}

func TestAcceptStartsTheAttempt(t *testing.T) {
	f := newFixture(t)
	id := f.createBuild(t)
	got := f.next(t)
	resp, err := f.engine.Runners().Accept(t.Context(), &protocol.AcceptRequest{
		WorkflowId: got.GetWorkflowId(), Version: got.GetVersion(),
	})
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	if resp.GetAttemptId() != 1 {
		t.Errorf("Accept() attempt = %d, want 1", resp.GetAttemptId())
	}
	w := f.build(t, id).Workflows[0]
	if w.State != model.WorkflowRunning {
		t.Errorf("workflow = %s, want running", w.State)
	}
	if len(w.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(w.Attempts))
	}
	names := make([]string, 0, len(w.Attempts[0].Steps))
	for _, s := range w.Attempts[0].Steps {
		names = append(names, s.Name)
	}
	if want := []string{"checkout", "build", "test"}; !slices.Equal(names, want) {
		t.Errorf("steps = %q, want %q", names, want)
	}
}

func TestAcceptAtAnotherVersionConflicts(t *testing.T) {
	f := newFixture(t)
	id := f.createBuild(t)
	got := f.next(t)
	_, err := f.engine.Runners().Accept(t.Context(), &protocol.AcceptRequest{
		WorkflowId: got.GetWorkflowId(), Version: 1,
	})
	if !errors.Is(err, model.ErrConflict) {
		t.Errorf("Accept() at version 1 error = %v, want %v", err, model.ErrConflict)
	}
	if w := f.build(t, id).Workflows[0]; w.State != model.WorkflowAssigned || len(w.Attempts) != 0 {
		t.Errorf("workflow = %s with %d attempts, want assigned with none", w.State, len(w.Attempts))
	}
}

func TestAcceptUnknownWorkflow(t *testing.T) {
	f := newFixture(t)
	_, err := f.engine.Runners().Accept(t.Context(), &protocol.AcceptRequest{WorkflowId: 42, Version: 2})
	if !errors.Is(err, model.ErrNotFound) {
		t.Errorf("Accept() of an unknown workflow error = %v, want %v", err, model.ErrNotFound)
	}
}

func TestNextWithNoWorkReturnsNothingAfterTheWindow(t *testing.T) {
	f := newFixture(t)
	start := time.Now()
	got := f.next(t)
	if got.GetWorkflow() != nil || got.GetWorkflowId() != 0 {
		t.Errorf("Next() = %v, want no workflow", got)
	}
	if waited := time.Since(start); waited < testWindow {
		t.Errorf("Next() returned after %v, want the window, %v", waited, testWindow)
	}
}

func TestNextReturnsABuildCreatedWhileItWaits(t *testing.T) {
	f := newFixture(t)
	f.engine.window = time.Minute
	type result struct {
		resp *protocol.NextResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := f.engine.Runners().Next(t.Context(), &protocol.NextRequest{})
		done <- result{resp, err}
	}()
	time.Sleep(100 * time.Millisecond)
	id := f.createBuild(t)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Next() error = %v", r.err)
		}
		if w := f.build(t, id).Workflows[0]; r.resp.GetWorkflowId() != int64(w.ID) {
			t.Errorf("Next() = workflow %d, want the new build's %d", r.resp.GetWorkflowId(), w.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next() did not return the build created while it waited")
	}
}

func TestNextCancelledWhileWaitingAssignsNothing(t *testing.T) {
	f := newFixture(t)
	f.engine.window = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := f.engine.Runners().Next(ctx, &protocol.NextRequest{})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Next() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next() did not return once its context was cancelled")
	}
	id := f.createBuild(t)
	time.Sleep(1500 * time.Millisecond)
	if w := f.build(t, id).Workflows[0]; w.State != model.WorkflowPending {
		t.Errorf("workflow = %s, want pending", w.State)
	}
}

func TestNextWithACancelledContextAssignsNothing(t *testing.T) {
	f := newFixture(t)
	id := f.createBuild(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.engine.Runners().Next(ctx, &protocol.NextRequest{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Next() error = %v, want %v", err, context.Canceled)
	}
	if w := f.build(t, id).Workflows[0]; w.State != model.WorkflowPending {
		t.Errorf("workflow = %s, want pending", w.State)
	}
}

func TestNextOnAStoppedEngineAssignsNothing(t *testing.T) {
	f := newFixture(t)
	id := f.createBuild(t)
	f.stop()
	got := f.next(t)
	if got.GetWorkflow() != nil {
		t.Errorf("Next() on a stopped engine = %v, want no workflow", got)
	}
	if w := f.build(t, id).Workflows[0]; w.State != model.WorkflowPending {
		t.Errorf("workflow = %s, want pending", w.State)
	}
}

func TestStoppingTheEngineEndsAWaitingNext(t *testing.T) {
	f := newFixture(t)
	f.engine.window = time.Minute
	done := make(chan *protocol.NextResponse, 1)
	go func() {
		resp, err := f.engine.Runners().Next(t.Context(), &protocol.NextRequest{})
		if err != nil {
			t.Errorf("Next() error = %v", err)
		}
		done <- resp
	}()
	time.Sleep(100 * time.Millisecond)
	f.stop()
	select {
	case resp := <-done:
		if resp.GetWorkflow() != nil {
			t.Errorf("Next() = %v, want no workflow", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next() did not return once the engine stopped")
	}
}
