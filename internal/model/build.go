package model

import "time"

// BuildID identifies a build.
type BuildID int64

// WorkflowID identifies a workflow.
type WorkflowID int64

// AttemptID identifies an attempt.
type AttemptID int64

// StepID identifies a step.
type StepID int64

// Build is the snapshot of a build, with all its workflows.
type Build struct {
	ID    BuildID
	State BuildState
	// Outcome is nil until the build is finished.
	Outcome    *Outcome
	CreatedAt  time.Time
	FinishedAt *time.Time
	Workflows  []Workflow
}

// Workflow is the snapshot of a workflow, with every attempt.
type Workflow struct {
	ID WorkflowID
	// Name is unique within the build.
	Name  string
	State WorkflowState
	// Outcome is nil until the workflow is finished.
	Outcome *Outcome
	// Version grows each time the workflow is handed out.
	Version int64
	// Definition is the workflow as the runner receives it.
	Definition []byte
	CreatedAt  time.Time
	AssignedAt *time.Time
	FinishedAt *time.Time
	Attempts   []Attempt
}

// Attempt is the snapshot of one run of a workflow, with its steps.
type Attempt struct {
	ID AttemptID
	// Number counts the workflow's attempts from 1.
	Number int
	// AcceptedVersion is the workflow's version when the attempt started.
	AcceptedVersion int64
	// Outcome is nil while the attempt is open.
	Outcome    *Outcome
	StartedAt  time.Time
	FinishedAt *time.Time
	Steps      []Step
}

// Step is the snapshot of one step of an attempt.
type Step struct {
	ID StepID
	// Position is the step's place in its attempt, unique within it.
	Position int
	Name     string
	State    StepState
	// Outcome is nil unless the step is finished.
	Outcome *Outcome
	// ExitStatus is nil when the step reported none.
	ExitStatus *int
	StartedAt  *time.Time
	FinishedAt *time.Time
}
