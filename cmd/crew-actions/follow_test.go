package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// twoSteps is a workflow file whose two steps succeed.
const twoSteps = `steps:
  - name: build
    commands: ["echo built"]
  - name: test
    commands: ["echo tested"]
`

// repo is the identity the tests submit with.
var repo = model.Repository{Host: "gitlab.com", Organization: "group/sub", Name: "repo"}

// Events of a build, for the fake engine to send.
func workflowEvent(state api.WorkflowState, outcome api.Outcome) *api.FollowResponse {
	return &api.FollowResponse{Event: &api.FollowResponse_Workflow{Workflow: &api.WorkflowEvent{
		Name: "test", State: state, Outcome: outcome,
	}}}
}

func stepEvent(attempt, position int32, name string, state api.StepState, outcome api.Outcome) *api.FollowResponse {
	e := &api.StepEvent{Attempt: attempt, Position: position, Name: name, State: state, Outcome: outcome}
	if outcome == api.Outcome_OUTCOME_FAILURE {
		e.ExitStatus = new(int32(1))
	}
	return &api.FollowResponse{Event: &api.FollowResponse_Step{Step: e}}
}

// outputEvent is output of the first step.
func outputEvent(attempt int32, offset int64, data string) *api.FollowResponse {
	return &api.FollowResponse{Event: &api.FollowResponse_Output{Output: &api.OutputEvent{
		Attempt: attempt, Position: 1, Offset: offset, Data: []byte(data),
	}}}
}

func endEvent(outcome api.Outcome) *api.FollowResponse {
	return &api.FollowResponse{Event: &api.FollowResponse_End{End: &api.BuildEnd{Outcome: outcome}}}
}

// failedBuild is a build whose second step fails, after printing a line
// and a partial one.
var failedBuild = []*api.FollowResponse{
	workflowEvent(api.WorkflowState_WORKFLOW_STATE_PENDING, api.Outcome_OUTCOME_UNSPECIFIED),
	workflowEvent(api.WorkflowState_WORKFLOW_STATE_RUNNING, api.Outcome_OUTCOME_UNSPECIFIED),
	stepEvent(1, 1, "build", api.StepState_STEP_STATE_RUNNING, api.Outcome_OUTCOME_UNSPECIFIED),
	outputEvent(1, 0, "one\ntw"),
	outputEvent(1, 6, "o\nthr"),
	outputEvent(1, 11, "ee"),
	stepEvent(1, 1, "build", api.StepState_STEP_STATE_FINISHED, api.Outcome_OUTCOME_FAILURE),
	stepEvent(1, 2, "test", api.StepState_STEP_STATE_SKIPPED, api.Outcome_OUTCOME_UNSPECIFIED),
	workflowEvent(api.WorkflowState_WORKFLOW_STATE_FINISHED, api.Outcome_OUTCOME_FAILURE),
	endEvent(api.Outcome_OUTCOME_FAILURE),
}

// fakeBuilds is an engine that sends events to every follow.
type fakeBuilds struct {
	submitErr error
	submitted *api.SubmitRequest
	events    []*api.FollowResponse
	// followErr is what Follow returns after the events, and onFollow is
	// called then. Without either and without an end among the events,
	// Follow waits for its context to end.
	followErr error
	onFollow  func()
}

func (f *fakeBuilds) Submit(_ context.Context, req *api.SubmitRequest) (*api.SubmitResponse, error) {
	f.submitted = req
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	return &api.SubmitResponse{BuildId: 7}, nil
}

func (f *fakeBuilds) Follow(ctx context.Context, _ *api.FollowRequest, send func(*api.FollowResponse) error) error {
	for _, e := range f.events {
		if err := send(e); err != nil {
			return err
		}
		if e.GetEnd() != nil {
			return nil
		}
	}
	if f.onFollow != nil {
		f.onFollow()
	}
	if f.followErr != nil {
		return f.followErr
	}
	<-ctx.Done()
	return ctx.Err() //nolint:wrapcheck // the real client returns its context's error as it is
}

// submit runs submitAndFollow against f, with a workflow file named
// test.yaml, and returns its exit code and output.
func submit(ctx context.Context, t *testing.T, f *fakeBuilds) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := submitAndFollow(ctx, f, repo, filepath.Join("ci", "test.yaml"), []byte(twoSteps), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRenderer(t *testing.T) {
	var out bytes.Buffer
	r := newRenderer(&out)
	events := append([]*api.FollowResponse{}, failedBuild[:len(failedBuild)-1]...)
	events = append(events,
		stepEvent(2, 1, "build", api.StepState_STEP_STATE_RUNNING, api.Outcome_OUTCOME_UNSPECIFIED),
		outputEvent(2, 0, "again\n"),
		stepEvent(2, 1, "build", api.StepState_STEP_STATE_FINISHED, api.Outcome_OUTCOME_SUCCESS),
		workflowEvent(api.WorkflowState_WORKFLOW_STATE_ASSIGNED, api.Outcome_OUTCOME_UNSPECIFIED),
	)
	for _, e := range events {
		if err := r.handle(e); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
	}
	want := `workflow test: pending, waiting for a runner
workflow test: running
build: running
build | one
build | two
build | three
build: finished with failure, exit status 1
test: skipped
workflow test: finished with failure
build (attempt 2): running
build (attempt 2) | again
build (attempt 2): finished with success
workflow test: assigned to a runner
`
	if out.String() != want {
		t.Errorf("the renderer printed\n%s\nwant\n%s", out.String(), want)
	}
}

func TestRendererHoldsAPartialLine(t *testing.T) {
	var out bytes.Buffer
	r := newRenderer(&out)
	for _, e := range failedBuild[2:5] {
		if err := r.handle(e); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
	}
	if want := "build: running\nbuild | one\nbuild | two\n"; out.String() != want {
		t.Errorf("before the step's end the renderer printed %q, want %q", out.String(), want)
	}
}

func TestRendererFailedOutput(t *testing.T) {
	r := newRenderer(failingWriter{})
	if err := r.handle(failedBuild[0]); err == nil {
		t.Error("handle() with a failing output = nil, want an error")
	}
}

func TestRunExitsWithTheBuildsOutcome(t *testing.T) {
	succeeded := []*api.FollowResponse{
		workflowEvent(api.WorkflowState_WORKFLOW_STATE_FINISHED, api.Outcome_OUTCOME_SUCCESS),
		endEvent(api.Outcome_OUTCOME_SUCCESS),
	}
	for name, tc := range map[string]struct {
		events []*api.FollowResponse
		want   int
	}{
		"failure": {events: failedBuild, want: exitFailure},
		"success": {events: succeeded, want: exitOK},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeBuilds{events: tc.events}
			code, stdout, stderr := submit(t.Context(), t, f)
			if code != tc.want {
				t.Fatalf("run = %d, want %d; stderr %q", code, tc.want, stderr)
			}
			if !strings.HasPrefix(stdout, "build 7 of gitlab.com/group/sub/repo\n") {
				t.Errorf("stdout = %q, want it to start with the build's id", stdout)
			}
			if !strings.Contains(stdout, "build 7 finished with "+name) {
				t.Errorf("stdout = %q, want the build's outcome", stdout)
			}
			sub := f.submitted
			if sub.GetHost() != "gitlab.com" || sub.GetOrganization() != "group/sub" || sub.GetRepository() != "repo" ||
				sub.GetFileName() != "test.yaml" || string(sub.GetContent()) != twoSteps {
				t.Errorf("the engine got %v, want the file test.yaml of gitlab.com/group/sub/repo", sub)
			}
		})
	}
}

func TestRunRefusedFile(t *testing.T) {
	f := &fakeBuilds{submitErr: model.Invalid("submit test.yaml: depends_on (line 3): not supported yet")}
	code, stdout, stderr := submit(t.Context(), t, f)
	if code != exitUsage {
		t.Fatalf("run with a refused file = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "depends_on (line 3): not supported yet") {
		t.Errorf("stderr = %q, want the engine's refusal", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestRunSubmitFails(t *testing.T) {
	f := &fakeBuilds{submitErr: model.ErrUnavailable}
	code, _, stderr := submit(t.Context(), t, f)
	if code != exitFailure {
		t.Fatalf("run with an unavailable engine = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "unknown") {
		t.Errorf("stderr = %q, want it to say whether a build was created is unknown", stderr)
	}
}

func TestRunFollowFails(t *testing.T) {
	for name, err := range map[string]error{
		"unknown build": model.ErrNotFound,
		"engine error":  errors.New("the engine failed the call"),
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := submit(t.Context(), t, &fakeBuilds{followErr: err})
			if code != exitFailure {
				t.Fatalf("run = %d, want %d", code, exitFailure)
			}
			if !strings.Contains(stderr, "build 7") {
				t.Errorf("stderr = %q, want it to name the build", stderr)
			}
		})
	}
}

func TestRunInterruptedWhileFollowing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f := &fakeBuilds{events: failedBuild[:1], onFollow: cancel}
	code, stdout, stderr := submit(ctx, t, f)
	if code != exitInterrupted {
		t.Fatalf("an interrupted run = %d, want %d", code, exitInterrupted)
	}
	if !strings.Contains(stdout, "pending, waiting for a runner") {
		t.Errorf("stdout = %q, want what came before the interrupt", stdout)
	}
	if !strings.Contains(stderr, "build 7 continues") {
		t.Errorf("stderr = %q, want it to say the build continues", stderr)
	}
}

func TestRunUsage(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(file, []byte(twoSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run"},
		{"run", file, file},
		{"run", "--engine", "localhost:7700", file},
		{"run", "--repo", "gitlab.com/repo", file},
		{"run", "--repo", "gitlab.com/group/repo", filepath.Join(dir, "missing.yaml")},
	} {
		var stderr bytes.Buffer
		if got := run(t.Context(), args, &bytes.Buffer{}, &stderr); got != exitUsage {
			t.Errorf("run(%q) = %d, want %d; stderr %q", args, got, exitUsage, stderr.String())
		}
	}
}
