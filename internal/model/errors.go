package model

import "errors"

// The store's errors, which callers branch on with errors.Is.
var (
	// ErrConflict is returned when a row is not in the state a transition
	// moves it from, including when the transition was already made.
	ErrConflict = errors.New("conflict")
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
)
