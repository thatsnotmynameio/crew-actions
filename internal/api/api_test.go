package api

import (
	"errors"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// testEnum checks that toModel turns each value of known into its model
// value and fromModel turns it back, that toModel refuses UNSPECIFIED and a
// number the proto does not name with model.ErrInvalid, and that fromModel
// turns a model value it does not know into UNSPECIFIED.
func testEnum[P ~int32, M ~string](t *testing.T, toModel func(P) (M, error), fromModel func(M) P, known map[P]M) {
	t.Helper()
	for p, want := range known {
		got, err := toModel(p)
		if err != nil {
			t.Errorf("toModel(%d) error = %v, want nil", p, err)
		} else if got != want {
			t.Errorf("toModel(%d) = %q, want %q", p, got, want)
		}
		if back := fromModel(want); back != p {
			t.Errorf("fromModel(%q) = %d, want %d", want, back, p)
		}
	}
	for _, p := range []P{0, 99} {
		if got, err := toModel(p); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("toModel(%d) = %q, %v, want an error matching model.ErrInvalid", p, got, err)
		}
	}
	if got := fromModel(""); got != 0 {
		t.Errorf(`fromModel("") = %d, want 0`, got)
	}
}

func TestWorkflowState(t *testing.T) {
	testEnum(t, WorkflowStateToModel, WorkflowStateFromModel, map[WorkflowState]model.WorkflowState{
		WorkflowState_WORKFLOW_STATE_PENDING:  "pending",
		WorkflowState_WORKFLOW_STATE_ASSIGNED: "assigned",
		WorkflowState_WORKFLOW_STATE_RUNNING:  "running",
		WorkflowState_WORKFLOW_STATE_FINISHED: "finished",
	})
}

func TestStepState(t *testing.T) {
	testEnum(t, StepStateToModel, StepStateFromModel, map[StepState]model.StepState{
		StepState_STEP_STATE_PENDING:  "pending",
		StepState_STEP_STATE_RUNNING:  "running",
		StepState_STEP_STATE_FINISHED: "finished",
		StepState_STEP_STATE_SKIPPED:  "skipped",
	})
}

func TestOutcome(t *testing.T) {
	testEnum(t, OutcomeToModel, OutcomeFromModel, map[Outcome]model.Outcome{
		Outcome_OUTCOME_SUCCESS: "success",
		Outcome_OUTCOME_FAILURE: "failure",
	})
}
