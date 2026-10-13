// Package model types the lifecycle of a build: its states, its outcomes,
// the snapshot of a build the store returns, and the store's errors.
//
// A build moves running → finished, and takes its workflow's outcome.
//
// A workflow moves pending → assigned → running → finished. Its state is
// separate from its outcome, which it has only once finished. Why a pending
// workflow waits is not a state.
//
// A step moves pending → running → finished within one attempt of its
// workflow, or ends skipped.
//
// An outcome is success or failure only.
//
// The package does no I/O.
package model
