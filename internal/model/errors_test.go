package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestInvalid(t *testing.T) {
	const msg = "workflow build: line 3: unknown key \"step\""
	err := fmt.Errorf("submit: %w", Invalid(msg))
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("errors.Is(%v, ErrInvalid) = false, want true", err)
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) {
		t.Errorf("%v matches a sentinel other than ErrInvalid", err)
	}
	if got := Invalid(msg).Error(); got != msg {
		t.Errorf("Invalid(%q).Error() = %q, want the message", msg, got)
	}
}
