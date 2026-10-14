// Package api is the engine's API for clients, such as run, apart from the
// runner protocol: BuildService's messages, generated from
// proto/crewactions/api/v1/api.proto, the Builds interface, and the
// conversions of the API's enums to internal/model's types.
//
// A client submits a workflow file, then follows the build it created to
// its end. The connect code, which serves and calls BuildService over HTTP,
// is generated under internal/transport/connect, the one package that
// imports connect-go.
package api

import (
	"context"
	"fmt"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// Builds is the engine as a client sees it: BuildService's calls, which the
// engine implements and a client calls through a transport.
type Builds interface {
	// Submit creates a build from one workflow file, and creates nothing
	// when it refuses the file.
	Submit(ctx context.Context, req *SubmitRequest) (*SubmitResponse, error)
	// Follow calls send with each event of the build, from its start and
	// in order, and returns once it sent the build's end. It returns send's
	// error when send fails.
	Follow(ctx context.Context, req *FollowRequest, send func(*FollowResponse) error) error
}

// WorkflowStateToModel returns the model's workflow state for s, and an
// error matching model.ErrInvalid for WORKFLOW_STATE_UNSPECIFIED or a number
// the API does not name.
func WorkflowStateToModel(s WorkflowState) (model.WorkflowState, error) {
	switch s {
	case WorkflowState_WORKFLOW_STATE_PENDING:
		return model.WorkflowPending, nil
	case WorkflowState_WORKFLOW_STATE_ASSIGNED:
		return model.WorkflowAssigned, nil
	case WorkflowState_WORKFLOW_STATE_RUNNING:
		return model.WorkflowRunning, nil
	case WorkflowState_WORKFLOW_STATE_FINISHED:
		return model.WorkflowFinished, nil
	default:
		return "", invalid("workflow state", s)
	}
}

// WorkflowStateFromModel returns the API's workflow state for s, and
// WORKFLOW_STATE_UNSPECIFIED for a state it does not know.
func WorkflowStateFromModel(s model.WorkflowState) WorkflowState {
	switch s {
	case model.WorkflowPending:
		return WorkflowState_WORKFLOW_STATE_PENDING
	case model.WorkflowAssigned:
		return WorkflowState_WORKFLOW_STATE_ASSIGNED
	case model.WorkflowRunning:
		return WorkflowState_WORKFLOW_STATE_RUNNING
	case model.WorkflowFinished:
		return WorkflowState_WORKFLOW_STATE_FINISHED
	default:
		return WorkflowState_WORKFLOW_STATE_UNSPECIFIED
	}
}

// StepStateToModel returns the model's step state for s, and an error
// matching model.ErrInvalid for STEP_STATE_UNSPECIFIED or a number the API
// does not name.
func StepStateToModel(s StepState) (model.StepState, error) {
	switch s {
	case StepState_STEP_STATE_PENDING:
		return model.StepPending, nil
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

// StepStateFromModel returns the API's step state for s, and
// STEP_STATE_UNSPECIFIED for a state it does not know.
func StepStateFromModel(s model.StepState) StepState {
	switch s {
	case model.StepPending:
		return StepState_STEP_STATE_PENDING
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
// model.ErrInvalid for OUTCOME_UNSPECIFIED or a number the API does not
// name.
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

// OutcomeFromModel returns the API's outcome for o, and OUTCOME_UNSPECIFIED
// for an outcome it does not know.
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
