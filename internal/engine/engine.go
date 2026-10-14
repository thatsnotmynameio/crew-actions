// Package engine holds the engine's use cases and owns their transactions.
//
// One core, Engine, serves runners through Runners, which implements
// protocol.Engine: Next hands out the oldest pending workflow, Accept
// starts its attempt, Update and Log record its steps' states and output,
// and Done ends it. It serves clients through Clients, which implements
// api.Builds: Submit creates a build from a workflow file, and Follow
// streams the build's states and output to its end.
//
// The engine writes through the store and log interfaces it declares here,
// which internal/store/postgres and internal/store/logs implement. Every
// write fires an in-process notifier, which wakes what waits for a change.
//
// The package imports no transport, backend or source.
package engine

import (
	"context"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// The hand-out's timings (KTD5, KTD6).
const (
	// window is how long Next waits for work before it returns nothing.
	window = 25 * time.Second
	// tick is how often Next looks for work, and Follow at the build, again
	// with no change notified.
	tick = time.Second
)

// Store is the store the engine reads and writes, one interface per
// aggregate. Each call is atomic alone and joins the transaction InTx puts
// in its context.
type Store interface {
	// InTx runs fn with a transaction in its context, and commits it when
	// fn returns nil.
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	Workflows
	Attempts
	Builds
}

// Workflows hands workflows out and finishes them.
type Workflows interface {
	// PendingWorkflows returns up to n pending workflows, oldest first,
	// locked until the transaction ends.
	PendingWorkflows(ctx context.Context, n int) ([]model.Candidate, error)
	// AssignWorkflow hands out the workflow id at version, and returns its
	// new version and its definition.
	AssignWorkflow(ctx context.Context, id model.WorkflowID, version int64) (int64, []byte, error)
	// WorkflowDefinition returns the definition of the workflow id.
	WorkflowDefinition(ctx context.Context, id model.WorkflowID) ([]byte, error)
	// FinishWorkflow moves the workflow id to finished with outcome.
	FinishWorkflow(ctx context.Context, id model.WorkflowID, outcome model.Outcome) error
}

// Attempts opens a workflow's attempts, moves their steps and ends them.
type Attempts interface {
	// StartAttempt accepts the workflow at version and opens its attempt
	// with a pending step for each of steps.
	StartAttempt(ctx context.Context, workflow model.WorkflowID, version int64, steps []string) (model.AttemptID, error)
	// AttemptWorkflow returns the workflow and the build of the attempt
	// id, and its number.
	AttemptWorkflow(ctx context.Context, id model.AttemptID) (model.WorkflowID, model.BuildID, int, error)
	// StartStep moves the step at position of the attempt to running.
	StartStep(ctx context.Context, attempt model.AttemptID, position int) error
	// FinishStep moves the step at position of the attempt to finished
	// with outcome and exitStatus, nil for none.
	FinishStep(ctx context.Context, attempt model.AttemptID, position int, outcome model.Outcome, exitStatus *int) error
	// SkipStep moves the step at position of the attempt to skipped.
	SkipStep(ctx context.Context, attempt model.AttemptID, position int) error
	// FinishAttempt ends the attempt id with outcome.
	FinishAttempt(ctx context.Context, id model.AttemptID, outcome model.Outcome) error
}

// Builds creates builds, reads them and finishes them.
type Builds interface {
	// CreateBuild creates a running build of repo with workflows, each
	// pending, and returns its id.
	CreateBuild(ctx context.Context, repo model.Repository, workflows []model.NewWorkflow) (model.BuildID, error)
	// GetBuild returns the snapshot of the build id, with every workflow,
	// attempt and step, read in one consistent snapshot when alone.
	GetBuild(ctx context.Context, id model.BuildID) (model.Build, error)
	// FinishBuild moves the build id to finished with outcome.
	FinishBuild(ctx context.Context, id model.BuildID, outcome model.Outcome) error
}

// Logs holds each step's output, one log per step and attempt.
type Logs interface {
	// Create creates the log of the step at position of the attempt if it
	// is absent, and never truncates one that exists.
	Create(attempt model.AttemptID, position int) error
	// Append writes the bytes of data, which starts at offset, that the log
	// does not hold yet, and returns how many bytes it holds then.
	Append(attempt model.AttemptID, position int, offset int64, data []byte) (int64, error)
	// Size returns how many bytes the log holds, 0 when it does not exist.
	Size(attempt model.AttemptID, position int) (int64, error)
	// Read returns at most limit bytes of the log from offset on, none at
	// its end or when it does not exist.
	Read(attempt model.AttemptID, position int, offset int64, limit int) ([]byte, error)
}

// Engine is the engine's core. It is safe for concurrent use.
type Engine struct {
	store  Store
	logs   Logs
	notify *notifier
	// stopped is closed once the engine stops.
	stopped <-chan struct{}
	// window is how long Next waits for work, and tick how often Next and
	// Follow look again with no change notified.
	window time.Duration
	tick   time.Duration
}

// New returns the engine on store and logs. Cancelling stop stops it: Next
// then returns nothing without handing a workflow out, and Follow returns
// model.ErrUnavailable.
func New(stop context.Context, store Store, logs Logs) *Engine {
	return &Engine{
		store:   store,
		logs:    logs,
		notify:  newNotifier(),
		stopped: stop.Done(),
		window:  window,
		tick:    tick,
	}
}

// Runners returns the engine as runners see it.
func (e *Engine) Runners() *Runners {
	return &Runners{engine: e}
}

// Runners is the engine as runners see it: it implements protocol.Engine.
type Runners struct {
	engine *Engine
}

var _ protocol.Engine = (*Runners)(nil)

// Clients returns the engine as clients see it.
func (e *Engine) Clients() *Clients {
	return &Clients{engine: e}
}

// Clients is the engine as clients see it: it implements api.Builds.
type Clients struct {
	engine *Engine
}

var _ api.Builds = (*Clients)(nil)
