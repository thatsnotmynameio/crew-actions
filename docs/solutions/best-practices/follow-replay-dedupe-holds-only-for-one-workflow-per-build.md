---
title: api.Follow's replay dedupe holds only while a build has one workflow and the engine replays workflow states as a snapshot
date: 2026-10-14
category: best-practices
module: internal/api
problem_type: best_practice
component: api_layer
severity: medium
applies_when:
  - A build can hold more than one workflow (slice 2, depends_on, issue #6)
  - A change touches StepEvent, OutputEvent or WorkflowEvent in proto/crewactions/api/v1/api.proto
  - A change touches the engine's side of Follow (internal/engine/follow.go, from #74), above all what a fresh stream sends for a workflow
  - A change touches the follower's keys in internal/api/follow.go
  - A plan cites KTD9's "keyed by attempt and position" from the plans for #72 or #77
tags: [follow, replay, dedupe, workflow-identity, attempt, position, ktd9, slice-2]
---

# api.Follow's replay dedupe holds only while a build has one workflow and the engine replays workflow states as a snapshot

## Context

Issue #77 added `api.Follow` (`internal/api/follow.go`). It calls the engine's `Follow` stream and calls it again every second on `model.ErrUnavailable`. Every stream starts from the build's start (`internal/api/api.go:25`), so the follower drops what it already handed on. KTD9, in the plans for #72 and #77, says the client dedupes "keyed by attempt and position" (`docs/plans/2026-10-14-0215-issue-72-plan.md:71`, `docs/plans/2026-10-14-0549-issue-77-plan.md:48`). Neither plan says when that key stops being enough. Plans are not updated after they ship.

The follower keys its three kinds of event like this:

| Event | Key | Kept as | Where |
|---|---|---|---|
| `StepEvent` | attempt, position, state | a set (`steps map[stepState]bool`) | `internal/api/follow.go:86`, `:124` |
| `OutputEvent` | attempt, position | bytes handed on so far (`output map[step]int64`) | `internal/api/follow.go:88`, `:139` |
| `WorkflowEvent` | workflow name | the last state handed on (`workflows map[string]WorkflowState`) | `internal/api/follow.go:84`, `:118-122` |

The keys are this narrow because the protocol has nothing wider. `StepEvent` and `OutputEvent` carry an attempt and a position but no workflow (`proto/crewactions/api/v1/api.proto:84-106`). `WorkflowEvent` carries a name and a state but no attempt (`api.proto:76-81`).

Each key rests on an assumption that holds today. Nothing in the code or the tests checks either one.

1. **One workflow per build.** `Submit` takes one workflow file (`internal/api/api.go:22`). So (attempt, position) names one step of one workflow.
2. **A fresh stream sends a workflow's current state, not its history.** A workflow's state is kept as its last state, not as a set, so a workflow sent back to the queue shows pending and running again (`TestFollowDeliversASecondAttempt`). That works only because KTD9's engine diffs snapshots: a new stream's first pass sends the state it reads now. The engine's side, from #74 (merged in #99, after this branch's base), does so: `workflow` in `internal/engine/follow.go:138` sends a workflow's state only when it differs from the last one that stream sent, from an empty map. The replay in `TestFollowResumesAfterAnOutageWithoutRepeating` (`internal/api/follow_test.go:107`) starts at `RUNNING`, not at `PENDING`.

The review of #77 found the first assumption. Five Claude reviewers missed it. The Codex adversarial reviewer raised it as a P1, which the review demoted because no build can have two workflows yet (session history). Mutation testing could not catch it either: every fixture has one workflow, and no mutant can drop a workflow field the key never had (session history). The second assumption came from the same adversarial pass and was checked against the test fixtures for this doc.

## Guidance

**Before a build can hold several workflows (slice 2, #6), give step and output events a workflow identity, and key the follower on it.**

- Add the workflow's name, or another id that stays stable for the build, to `StepEvent` and `OutputEvent`.
- Widen the `step` key (`internal/api/follow.go:62-64`) to workflow, attempt and position. Both `steps` and `output` use it.
- Add a test with two workflows whose first steps share attempt 1 and position 1, with output at offset 0 in both. Today the second workflow's step states and its first bytes are dropped as repeats.

**Keep the engine's side of Follow (`internal/engine/follow.go`, from #74) sending a workflow's current state when a stream starts, never its past states.**

- If a fresh stream replays a workflow's history (pending, then running), the follower's last state is running, so it hands on pending and running again after every reconnect.
- If the engine must send history, the follower needs an attempt on `WorkflowEvent` and a set of (workflow, attempt, state), like the one steps have.

**Keep KTD9's ordering on the engine side.** The output trimming assumes each step's output arrives in offset order, with no gaps. `nextOutput` passes an event with a gap on unchanged (`internal/api/follow.go:138-153`), which was considered and not built because the planned server never sends one.

## Why This Matters

Both failures are silent. `Follow` returns the right outcome, the callback gets no error, and `run` prints a build with part of a workflow missing, or with a workflow's states printed twice after an engine restart. Each happens only with an input that cannot exist in this repository today: a second workflow, or an engine stream that replays history. So no existing test fails when slice 2 lands or the engine's stream changes, and nothing points back to `follow.go`.

## When to Apply

- When slice 2 (#6, `docs/architecture.md:214`) lets one build hold several workflows.
- When a change to `internal/engine/follow.go` touches what a fresh stream sends.
- When a change touches the Follow events in `proto/crewactions/api/v1/api.proto`, or the follower's keys.
- When a plan copies KTD9's "keyed by attempt and position". That holds for one workflow per build only.

## Examples

Two workflows, `build` and `test`, each with one step at position 1 in attempt 1. With today's keys:

```text
stream:   step(1,1,RUNNING) [build]  output(1,1,0,"compiling\n") [build]
          step(1,1,RUNNING) [test]   output(1,1,0,"ok\n")        [test]
handed:   step(1,1,RUNNING) [build]  output(1,1,0,"compiling\n") [build]
          -- dropped --              -- dropped: 3 <= 10 bytes already handed on --
```

An engine that replays a workflow's history on a fresh stream, after an outage while the workflow ran:

```text
before:   workflow(build, PENDING)  workflow(build, RUNNING)   last = RUNNING
replay:   workflow(build, PENDING)  -> PENDING != RUNNING, handed on again
          workflow(build, RUNNING)  -> RUNNING != PENDING, handed on again
```

## Related

- `docs/solutions/best-practices/connect-go-non-remote-error-does-not-mean-the-server-is-unreachable.md`: which errors become `model.ErrUnavailable`, the error `Follow` reconnects on. Its retry is now live in `api.Follow`.
- Issues: #77 (this follower), #74 and #99 (the engine's side of Follow), #6 (slice 2, several workflows per build), #72 (KTD9 first written), #22 (parent).
