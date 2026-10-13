package model

import (
	"errors"
	"fmt"
	"testing"
)

// unknown holds strings no parse function accepts: a state slice 2 adds,
// an empty column and a case variant.
var unknown = []string{"waiting", "", "Success"}

// testParse checks that parse turns each string of known into its value,
// and refuses every string of unknown.
func testParse[T ~string](t *testing.T, parse func(string) (T, error), known map[string]T) {
	t.Helper()
	for s, want := range known {
		got, err := parse(s)
		if err != nil {
			t.Errorf("parse(%q) error = %v, want nil", s, err)
			continue
		}
		if got != want {
			t.Errorf("parse(%q) = %q, want %q", s, got, want)
		}
	}
	for _, s := range unknown {
		if got, err := parse(s); err == nil {
			t.Errorf("parse(%q) = %q, want an error", s, got)
		}
	}
}

func TestParseBuildState(t *testing.T) {
	testParse(t, ParseBuildState, map[string]BuildState{
		"running":  BuildRunning,
		"finished": BuildFinished,
	})
}

func TestParseWorkflowState(t *testing.T) {
	testParse(t, ParseWorkflowState, map[string]WorkflowState{
		"pending":  WorkflowPending,
		"assigned": WorkflowAssigned,
		"running":  WorkflowRunning,
		"finished": WorkflowFinished,
	})
}

func TestParseStepState(t *testing.T) {
	testParse(t, ParseStepState, map[string]StepState{
		"pending":  StepPending,
		"running":  StepRunning,
		"finished": StepFinished,
		"skipped":  StepSkipped,
	})
}

func TestParseOutcome(t *testing.T) {
	testParse(t, ParseOutcome, map[string]Outcome{
		"success": OutcomeSuccess,
		"failure": OutcomeFailure,
	})
}

func TestSentinelsSurviveWrapping(t *testing.T) {
	sentinels := []error{ErrConflict, ErrNotFound}
	for i, sentinel := range sentinels {
		wrapped := fmt.Errorf("workflow 7: %w", sentinel)
		for j, other := range sentinels {
			if got, want := errors.Is(wrapped, other), i == j; got != want {
				t.Errorf("errors.Is(%q, %q) = %t, want %t", wrapped, other, got, want)
			}
		}
	}
}
