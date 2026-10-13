package model

import "errors"

// The errors the store, the engine and the transport share, which callers
// branch on with errors.Is.
var (
	// ErrConflict is returned when a row is not in the state a transition
	// moves it from, including when the transition was already made.
	ErrConflict = errors.New("conflict")
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid is matched by the errors Invalid makes, whose message is
	// safe to show to whoever made the request.
	ErrInvalid = errors.New("invalid")
	// ErrUnavailable is returned when the server is stopping or cannot be
	// reached.
	ErrUnavailable = errors.New("unavailable")
)

// Invalid returns an error that matches ErrInvalid and whose message is msg.
// msg must be safe to show to whoever made the request.
func Invalid(msg string) error {
	return &invalidError{msg: msg}
}

// invalidError is an ErrInvalid that carries a message to show.
type invalidError struct {
	msg string
}

func (e *invalidError) Error() string { return e.msg }

func (e *invalidError) Is(target error) bool { return target == ErrInvalid }
