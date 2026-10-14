// Package protocol is the contract between the engine and the runner, and
// the only thing the two share: RunnerService's messages, generated from
// proto/crewactions/protocol/v1/protocol.proto, the Engine interface, and
// the conversions of the protocol's enums to internal/model's types.
//
// A runner calls next, then accept with the version next returned, then
// update and log for each step in order, then done. Its connect code, which
// serves and calls RunnerService over HTTP, is generated under
// internal/transport/connect, the one package that imports connect-go.
package protocol

import (
	"context"
	"fmt"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// Engine is the engine as a runner sees it: RunnerService's calls, which the
// engine implements and the runner calls through a transport.
type Engine interface {
	// Next waits for a pending workflow and hands it out. Its response has
	// no workflow when none came within the engine's window.
	Next(ctx context.Context, req *NextRequest) (*NextResponse, error)
	// Accept starts an attempt of the workflow Next handed out, at the
	// version Next returned.
	Accept(ctx context.Context, req *AcceptRequest) (*AcceptResponse, error)
	// Update records a step's state.
	Update(ctx context.Context, req *UpdateRequest) (*UpdateResponse, error)
	// Log appends a chunk of a step's output at its offset.
	Log(ctx context.Context, req *LogRequest) (*LogResponse, error)
	// Done ends the attempt with its outcome.
	Done(ctx context.Context, req *DoneRequest) (*DoneResponse, error)
}

// StepStateToModel returns the model's step state for s, and an error
// matching model.ErrInvalid for STEP_STATE_UNSPECIFIED or a number the
// protocol does not name.
func StepStateToModel(s StepState) (model.StepState, error) {
	switch s {
	case StepState_STEP_STATE_RUNNING:
		return model.StepRunning, nil
	case StepState_STEP_STATE_FINISHED:
		return model.StepFinished, nil
	case StepState_STEP_STATE_SKIPPED:
		return model.StepSkipped, nil
	default:
		return "", invalid("step state", s)
	}
}

// StepStateFromModel returns the protocol's step state for s, and
// STEP_STATE_UNSPECIFIED for a state a runner never reports, such as
// pending.
func StepStateFromModel(s model.StepState) StepState {
	switch s {
	case model.StepRunning:
		return StepState_STEP_STATE_RUNNING
	case model.StepFinished:
		return StepState_STEP_STATE_FINISHED
	case model.StepSkipped:
		return StepState_STEP_STATE_SKIPPED
	default:
		return StepState_STEP_STATE_UNSPECIFIED
	}
}

// OutcomeToModel returns the model's outcome for o, and an error matching
// model.ErrInvalid for OUTCOME_UNSPECIFIED or a number the protocol does
// not name.
func OutcomeToModel(o Outcome) (model.Outcome, error) {
	switch o {
	case Outcome_OUTCOME_SUCCESS:
		return model.OutcomeSuccess, nil
	case Outcome_OUTCOME_FAILURE:
		return model.OutcomeFailure, nil
	default:
		return "", invalid("outcome", o)
	}
}

// OutcomeFromModel returns the protocol's outcome for o, and
// OUTCOME_UNSPECIFIED for an outcome it does not know.
func OutcomeFromModel(o model.Outcome) Outcome {
	switch o {
	case model.OutcomeSuccess:
		return Outcome_OUTCOME_SUCCESS
	case model.OutcomeFailure:
		return Outcome_OUTCOME_FAILURE
	default:
		return Outcome_OUTCOME_UNSPECIFIED
	}
}

// invalid returns the error for value v of the enum kind, which names v as
// the proto does, or by its number when the proto does not name it.
func invalid(kind string, v fmt.Stringer) error {
	return model.Invalid(fmt.Sprintf("invalid %s %s", kind, v)) //nolint:wrapcheck // model.Invalid makes this error
}
