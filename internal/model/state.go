package model

import "fmt"

// BuildState is where a build is in its lifecycle.
type BuildState string

// The build states.
const (
	BuildRunning  BuildState = "running"
	BuildFinished BuildState = "finished"
)

// WorkflowState is where a workflow is in its lifecycle.
type WorkflowState string

// The workflow states.
const (
	WorkflowPending  WorkflowState = "pending"
	WorkflowAssigned WorkflowState = "assigned"
	WorkflowRunning  WorkflowState = "running"
	WorkflowFinished WorkflowState = "finished"
)

// StepState is where a step is within its attempt.
type StepState string

// The step states.
const (
	StepPending  StepState = "pending"
	StepRunning  StepState = "running"
	StepFinished StepState = "finished"
	StepSkipped  StepState = "skipped"
)

// Outcome is how a finished build, workflow, attempt or step ended.
type Outcome string

// The outcomes.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// ParseBuildState returns the build state s names, and an error for any
// other string.
func ParseBuildState(s string) (BuildState, error) {
	return parse(s, "build state", BuildRunning, BuildFinished)
}

// ParseWorkflowState returns the workflow state s names, and an error for
// any other string.
func ParseWorkflowState(s string) (WorkflowState, error) {
	return parse(s, "workflow state", WorkflowPending, WorkflowAssigned, WorkflowRunning, WorkflowFinished)
}

// ParseStepState returns the step state s names, and an error for any other
// string.
func ParseStepState(s string) (StepState, error) {
	return parse(s, "step state", StepPending, StepRunning, StepFinished, StepSkipped)
}

// ParseOutcome returns the outcome s names, and an error for any other
// string.
func ParseOutcome(s string) (Outcome, error) {
	return parse(s, "outcome", OutcomeSuccess, OutcomeFailure)
}

// parse returns the value of known that s names, and an error naming kind
// when there is none.
func parse[T ~string](s, kind string, known ...T) (T, error) {
	for _, v := range known {
		if string(v) == s {
			return v, nil
		}
	}
	return "", fmt.Errorf("unknown %s %q", kind, s)
}
