---
title: A select on a process's exit and ctx.Done() must re-check the exit before reporting a cancellation
date: 2026-10-13
category: best-practices
module: internal/runner/backend/local
problem_type: best_practice
component: service_layer
severity: low
applies_when:
  - Code waits for a step's or command's process in a goroutine and selects on its exit and on ctx.Done()
  - The cancellation branch reports the process as stopped or cancelled instead of with its own result
  - A new backend (such as the docker backend) or the executor copies the waiting pattern of the local backend or of crew's shell adapter
  - A plan says to follow crew's internal/adapter/shell/shell.go for waiting and cancellation
root_cause: concurrency
tags: [select, context-cancellation, race, exit-status, proc, backend, ktd9]
---

# A select on a process's exit and ctx.Done() must re-check the exit before reporting a cancellation

## Context

Issue #35 added the local backend in `internal/runner/backend/local`. Its plan, `docs/plans/2026-10-13-0342-issue-35-plan.md`, says in U3's Approach: "Waiting and cancellation follow crew's shell adapter: `Wait` in a goroutine, a `select` on it and the context, then `Stop` with the grace context of KTD9". It names `internal/adapter/shell/shell.go` in the crew repository, not this one, as the pattern to follow. That file selects on the exit channel and on `ctx.Done()`. Its `ctx.Done()` branch calls `Stop`, drains the exit channel, drops what it received and returns `ctx.Err()`.

That shape breaks the plan's own KTD9: "When a step exits on its own before the cancellation is seen, its real result stands." When both channels are ready, a Go `select` picks one of the ready cases at random. `proc`'s `Stop` does nothing to a process that has already exited (`internal/proc/proc.go:128-133`). So a step that finished on its own is sometimes reported stopped instead of with its exit status, and the executor then reports it as stopped rather than as succeeded or failed.

The review of #35 (ce-code-review run `20261013-034912-5d2c7ea6`, finding #3, P3) found it by reading the code against the Go spec and `proc.go`. The timing was not reproduced, and no test pins it, because a test would have to make both cases ready at the same moment (session history). The fix is in `workspace.Run` (`internal/runner/backend/local/local.go:88-94`). Its comment states the rule but not why the re-check is needed. The plan still points at the racy shape, and in this repository's crew flow a plan is not updated once it ships.

## Guidance

In the cancellation branch, take the process's own result first when it is already there, and stop the process only when it is not:

```go
exited := make(chan error, 1)
go func() { exited <- p.Wait() }()
select {
case err := <-exited:
	return status(err)
case <-ctx.Done():
	// A step that exited before the cancellation was seen keeps its result.
	select {
	case err := <-exited:
		return status(err)
	default:
	}
	// ... Stop with the grace context, <-exited, return the cancellation error
}
```

The non-blocking `select` with a `default` is what turns the random choice into "exit wins when both are ready". Do not swap the order of the cases in the outer `select` instead: case order means nothing in Go.

## Why This Matters

A stopped step and a finished step mean different things downstream. KTD9 says that a cancelled attempt is stopped, not failed, and the plan leaves to part 5 (#23, recovering a workflow from a lost runner) what a stopped attempt means. A step that succeeded but is reported stopped can send an attempt down that path for no reason. The bug is rare and timing-dependent, so tests and CI will not catch a copy that leaves out the re-check. Only someone who knows to look for it will.

`internal/proc`'s `Group.Run` (`internal/proc/proc.go:196-224`) has the same shape: a `select` on `p.done` and `ctx.Done()` with no re-check, so a command that exited on its own can come back as an error wrapping `ctx.Err()`. It was not changed in #35. `proc` is a copy of crew's package (R20, `docs/plans/2026-10-13-0207-issue-33-plan.md`), so a change there is a deliberate change to the copy. `Process.Stop` in the same file already does the re-check (`proc.go:128-133`).

## When to Apply

- The docker backend (#12) and any other implementation of `backend.Workspace.Run`.
- The executor (#36), if it writes its own `select` on a step's completion and on the context.
- Any code that follows crew's shell adapter, or `proc.Group.Run`, as its pattern for waiting with cancellation.

## Examples

Before, as in crew's shell adapter and the first version of `workspace.Run`:

```go
select {
case err := <-exited:
	return status(err)
case <-ctx.Done():
	_ = p.Stop(stop) // a no-op when the process already exited
	<-exited         // its real result is dropped here
	return -1, fmt.Errorf("the step was stopped: %w", ctx.Err())
}
```

After: the same, with the non-blocking re-check shown under Guidance as the first statement of the `ctx.Done()` branch.

## Related

- Issue #35, Run an attempt's steps in a fresh directory with the local backend
- `docs/plans/2026-10-13-0342-issue-35-plan.md`, KTD9 and U3
- Issue #36, the executor, and issue #12, the containers slice, which will write the next waiting code
