package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
)

// stepKey names one step of one attempt.
type stepKey struct {
	attempt, position int32
}

// renderer prints a build's events as they come: each workflow's states on
// lines of their own, as "workflow <name>: <state>", each step's states as
// "<step>: <state>", and each line of a step's output as "<step> | <line>".
// Above the first attempt, the attempt joins the step's prefix. A step's
// last line, without its newline, is held until the step ends.
type renderer struct {
	out io.Writer
	// names holds each step's name.
	names map[stepKey]string
	// partial holds each step's output past its last newline.
	partial map[stepKey][]byte
}

func newRenderer(out io.Writer) *renderer {
	return &renderer{out: out, names: map[stepKey]string{}, partial: map[stepKey][]byte{}}
}

// handle prints the event e. A build's end prints nothing: run prints the
// outcome Follow returns.
func (r *renderer) handle(e *api.FollowResponse) error {
	switch ev := e.GetEvent().(type) {
	case *api.FollowResponse_Workflow:
		return r.printf("workflow %s: %s\n", ev.Workflow.GetName(), workflowState(ev.Workflow))
	case *api.FollowResponse_Step:
		return r.step(ev.Step)
	case *api.FollowResponse_Output:
		return r.output(ev.Output)
	}
	return nil
}

// step prints the step's new state, after the line it held when the step
// ended.
func (r *renderer) step(s *api.StepEvent) error {
	key := stepKey{attempt: s.GetAttempt(), position: s.GetPosition()}
	r.names[key] = s.GetName()
	switch s.GetState() {
	case api.StepState_STEP_STATE_FINISHED, api.StepState_STEP_STATE_SKIPPED:
		if held := r.partial[key]; len(held) > 0 {
			delete(r.partial, key)
			if err := r.printf("%s | %s\n", r.prefix(key), held); err != nil {
				return err
			}
		}
	default:
	}
	return r.printf("%s: %s\n", r.prefix(key), stepState(s))
}

// output prints each whole line of the step's output, and holds the rest.
func (r *renderer) output(o *api.OutputEvent) error {
	key := stepKey{attempt: o.GetAttempt(), position: o.GetPosition()}
	r.partial[key] = append(r.partial[key], o.GetData()...)
	rest := r.partial[key]
	for {
		line, after, found := bytes.Cut(rest, []byte("\n"))
		if !found {
			break
		}
		if err := r.printf("%s | %s\n", r.prefix(key), line); err != nil {
			return err
		}
		rest = after
	}
	r.partial[key] = rest
	return nil
}

// prefix returns the step's name, with its attempt above the first.
func (r *renderer) prefix(key stepKey) string {
	name, ok := r.names[key]
	if !ok {
		name = fmt.Sprintf("step %d", key.position)
	}
	if key.attempt > 1 {
		return fmt.Sprintf("%s (attempt %d)", name, key.attempt)
	}
	return name
}

func (r *renderer) printf(format string, args ...any) error {
	if _, err := fmt.Fprintf(r.out, format, args...); err != nil {
		return fmt.Errorf("print the build: %w", err)
	}
	return nil
}

// workflowState describes the workflow's state.
func workflowState(w *api.WorkflowEvent) string {
	switch w.GetState() {
	case api.WorkflowState_WORKFLOW_STATE_PENDING:
		return "pending, waiting for a runner"
	case api.WorkflowState_WORKFLOW_STATE_ASSIGNED:
		return "assigned to a runner"
	case api.WorkflowState_WORKFLOW_STATE_FINISHED:
		return "finished with " + outcomeName(w.GetOutcome())
	default:
		return stateName(w.GetState().String(), "WORKFLOW_STATE_")
	}
}

// stepState describes the step's state, with its outcome and an exit
// status other than 0 once it finished.
func stepState(s *api.StepEvent) string {
	if s.GetState() != api.StepState_STEP_STATE_FINISHED {
		return stateName(s.GetState().String(), "STEP_STATE_")
	}
	state := "finished with " + outcomeName(s.GetOutcome())
	if s.GetExitStatus() != 0 {
		state += fmt.Sprintf(", exit status %d", s.GetExitStatus())
	}
	return state
}

// outcomeName names the outcome o.
func outcomeName(o api.Outcome) string {
	return stateName(o.String(), "OUTCOME_")
}

// stateName returns the enum value's name, without its prefix, in lower
// case.
func stateName(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}
