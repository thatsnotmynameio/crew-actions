# crew actions: the engine

A draft to evolve, from a conversation on 2026-10-12. It is not a brainstorm yet.
A new actions engine, built alongside crew in a repository of its own: today's crew keeps working, and this design does not start from its rules, routes or labels.
The engine is simple and made for our case.

**Status:** exploration only.
This draft still goes through a brainstorm, then a plan, before any code is written, and either can change or drop anything here.
OpenTelemetry is decided (see [Measurement](#measurement)). The other libraries, tools and versions named below (pgx, sqlc, connect-rpc, the migration tool, docker compose, Postgres itself) are first candidates, not choices, and OpenTelemetry's version and exporters are open too: the brainstorm reviews each one and its version, since a better option may exist.

## The pieces

| Piece | Role |
|---|---|
| YAML (`when`, `steps`, `image`, `commands`, `settings`) | the syntax |
| The workflow as the queue's unit | what goes to a runner together and shares a workspace |
| `depends_on` between workflows | the order within a build; without it, everything starts at once |
| The pipeline as a definition, the build as a run | a set of files defines, each event makes a build |
| A runner that asks for work | labels, capacity, outgoing connections only |
| Hooks | what the runner does around a workflow |
| A container per step | where commands run, with the workspace mounted |
| Named queues with a limit | how many workflows of a kind run at once, on any runner |
| Multi-repo | one engine and the same runners for several repositories |

## Hierarchy

```text
engine (knows several repositories)
└── repository
    └── build (one run, started by an event)
        └── workflows   ← the queue's unit; depends_on between them; each goes whole to one runner
            └── steps   ← run together on the runner, in the same workspace, each in its own container
```

The engine receives the event, decides which workflows match, holds the queue, hands a workflow to whoever asks, receives states, logs and the result, releases the dependents and closes the build.
It never runs a step.
Even with a single runner, local and in the same process, the boundary stays: the engine hands out, the runner executes.

## Outcome: success or failure

A step and a workflow end only in success or failure.
Reacting to a failure is an ordinary step or workflow, with `when: status: failure`.
There are no verdicts, routes or states of crew's own.

## The YAML

One file per workflow, in `.crew/workflows/`.

```yaml
# .crew/workflows/test.yaml
when:
  - event: pull_request

queue: developer             # how many at once: the queue
labels:
  os: linux                  # where it runs: the runner
depends_on: [build]          # another workflow of the same build

steps:
  - name: test
    image: golang:1.27
    commands:
      - go test -race ./...

  - name: notify
    image: crew/comment      # a plugin is an image; settings arrive as environment variables
    settings:
      message: "the tests failed"
    when:
      - status: failure
```

`queue` and `labels` are separate: `labels` says where a workflow runs and `queue` says how many run at once.
`queue` never picks a runner: that is what `labels` do.

## The runner

```yaml
# on the runner's machine, outside any repository
name: notebook
labels: { os: linux }
capacity: 4
backend: docker              # or local
hooks: ~/.config/crew/hooks/
```

The runner asks the engine for work, so it only opens outgoing connections.
The capacity is the runner's: how many workflows it runs at once.
More parallelism means more runners or more capacity.

A workflow's path through the runner:

1. The runner asks for work and receives a workflow.
2. `pre-workflow` (a runner hook) can refuse the workflow: it is the policy point.
3. `environment` exports secrets and identity.
4. `checkout` creates the workspace; a hook can replace it.
5. Each step starts the container of its `image`, with the workspace mounted, and runs its `commands`.
6. `post-workflow`, the artifacts, and the result goes to the engine.

Hooks come from three places: the runner, the repository and a plugin.
The runner's hooks run on the machine.

## Queues and the match

When a runner asks for work, the engine hands it the first workflow that:

1. has every workflow it depends on finished;
2. has `labels` that match the runner's;
3. has a free slot in its queue;
4. comes first in the order: priority, then arrival.

The slot is taken at hand-out and freed at the end, or when the runner disappears and the workflow goes back to the queue.
Priority applies only after the slot: a workflow stuck in a full queue must not hold up one that has a slot in another queue.
A runner can serve several queues, so the order holds across every candidate it can take.

## Multi-repo

The engine is a service that knows several repositories, and the runners serve all of them.

```yaml
# the engine's configuration, outside any repository
repos:
  org/b: { priority: 10 }
  org/a: { priority: 0 }
queues:
  developer: 2
  clerk: 1
```

Queues live here, not in a repository: `developer: 2` holds for A and B at once.
Workflows only name their queue.

Priority is per project: with a workflow from A and one from B waiting in the same queue, B's goes first.
It is order, not dependency: neither workflow knows about the other.

## Database

Postgres from the start, and Postgres only.
Locally, a docker compose file in the repository starts it; the engine reads the database's URL from an environment variable, and as a service the same variable points at the production database.

Supporting several SQL dialects costs more than it gives: generated DDL per dialect, triggers to fake what one dialect lacks, or an ORM that hides the differences and the features.
One dialect lets the engine use what Postgres does well:

- **The database is the queue.** A workflow is claimed with `SELECT … FOR UPDATE SKIP LOCKED`, so a restart loses nothing and two engines never take the same row. A queue held in memory, with the database only as a backup, loses the running work on a restart.
- **A queue's limit is checked in the transaction that claims the workflow,** so "a slot is free" and "I took the slot" cannot race.
- **A lease column.** A runner renews its lease while it works; when a lease expires, the workflow goes back to the queue. Without a lease, a runner that dies leaves its workflow running forever.
- **A version column** for optimistic locking at accept: when two runners race for one workflow, the loser drops it.
- **LISTEN/NOTIFY** wakes the engine when something changes, instead of scanning every pending row on a timer.
- **Advisory locks** around each of the engine's loops, so several engines can run side by side, all alike, with no leader election.
- **Migrations** are plain `.sql` files embedded in the binary, run under an advisory lock at startup.

Tests of the store run against a real Postgres: the suite migrates a template database once, and each test creates its own database from the template (`CREATE DATABASE … TEMPLATE`), which is fast and parallel-safe.
The tests read the database's URL from the environment: locally from the compose file, in CI from a Postgres service container.

## Local and service

The local mode is the service with everything in one process, not a separate program.
Each thing that differs between the two is an interface from the first day:

| Seam | Local | Service |
|---|---|---|
| Database | Postgres from docker compose | a managed Postgres |
| Engine and runner | one process | the network, runners on other machines |
| Where events come from | polling, a manual start | webhooks: a service has a public URL |
| Identity | the machine's user and its `gh` token | runner registration tokens, API auth, per-repository credentials |
| Secrets | environment variables | stored encrypted in the database |
| Logs and artifacts | disk | disk or object storage |

Three things cost little now and a lot later:

1. **An API for clients (CLI, TUI, web), separate from the runner protocol.** Clients talk to the engine through it even in one process; otherwise a TUI that reads the engine's memory cannot work against a remote engine.
2. **Organization and repository on every row from the first migration.**
3. **The engine's configuration behind an interface:** a file locally, the database or an API as a service.

## Code architecture

### A repository of its own

crew actions lives in a repository of its own, apart from crew's, with its own Go module at its root.
A first candidate for its name is `thatsnotmynameio/crew-actions`, with the module path `github.com/thatsnotmynameio/crew-actions`; slice -1 settles it.
It is large and apart from crew, and its own repository makes the separation complete:

- **Its dependencies stay its own.** Postgres's driver, OpenTelemetry and connect-rpc enter its `go.mod`; crew's binary, its vulnerability scan and its dependency review do not grow with them.
- **The isolation is the repository's.** crew actions cannot import crew's packages, and crew does not import it. Inside the module, `depguard` checks the layers.
- **Its version, releases, CI and issues are its own,** with plain `vX.Y.Z` tags.
- **This draft started in crew's repository** (branch `docs/crew-actions-exploration`), as the exploration that led to this one, and is copied here. The architecture document of slice -1 and everything after it live in this repository.

What this brings, for the brainstorm of slice -1 to settle:

- **The binary.** A `crew-actions` binary, from `cmd/crew-actions`, and `crew actions …` as a git-style dispatch: crew runs `crew-actions` from the `PATH` with the rest of the arguments, so neither project requires the other. The commands keep the names used below (`crew actions run`, `crew actions engine`, `crew actions runner`). The dispatch is a small change in crew, made when there is something to dispatch to.
- **Starting a child process.** crew's `internal/proc` is crew's only way to do it, and another repository cannot import crew's `internal/`. crew actions gets its own, a copy to start with, or both use a small module they share.
- **The repository's setup, with crew's as the template:** CI with the same checks (`go mod tidy`, gofmt, vet, golangci-lint, `go test -race` with coverage, govulncheck), the security workflow, Dependabot, GoReleaser, the `checks` ruleset, an `AGENTS.md`, a README and a license (crew's is MIT).
- **Where the slices' issues live,** and whether crew runs the new repository's issues through its own rules, as it runs crew's.

### The tree

```text
crew-actions/              # the repository's root
  go.mod                   # github.com/thatsnotmynameio/crew-actions
  cmd/crew-actions/        # the binary: flags, environment, the SDK set up, wiring
  internal/
    spec/                  # YAML: parse, lint, compile into a neutral form
    spec/dag/              # the depends_on graph, with cycle detection
    model/                 # Build, Workflow, Step; states as types, not strings
    scheduler/             # pure: from a snapshot of the candidates, picks one
    engine/                # the use cases: create a build, hand out, accept, renew a lease, finish, release dependents
    store/postgres/        # pgx, embedded migrations, SKIP LOCKED, LISTEN/NOTIFY, advisory locks
    protocol/              # the engine–runner contract: protos and one Go interface
    transport/inproc/      # engine and runner in one process
    transport/connect/     # engine and runner on different machines
    api/                   # the API for the CLI, the TUI and the web, apart from the runner protocol
    source/github/         # where events come from
    runner/                # the poller and its capacity, the lease, cancellation, the reporter, logs
    runner/executor/       # the phases and hooks of one workflow
    runner/backend/        # the Backend interface: local, docker, dummy
    telemetry/             # every span, attribute and metric crew names, and crew's span exporter
```

### What each part does, and why

- **`spec/`** compiles the YAML into a neutral form before anything runs. The engine validates a whole build at once, and the runner receives something already resolved. `spec/dag/` rejects a cycle with a clear error when the files are read.
- **`model/`** types every state and outcome. A result kept as a string, or a magic image name that means "run on the host", spreads checks through the code.
- **`scheduler/`** is pure: from a read-only snapshot of the candidate workflows and the asking runner, it returns a choice, with no I/O. The match is a chain of small stages: filters (dependencies finished, labels, a free slot) and then a ranking (priority, then arrival). Each stage is tested alone, and stages can be reordered.
- **`engine/`** holds the use cases and owns the transactions: it loads the snapshot, asks the scheduler, and writes the choice in the same transaction. A workflow is born `waiting` when it has dependencies and `pending` when it has none. When it finishes, the engine moves the dependents whose dependencies are all finished to `pending`, and skips the ones a failure rules out. A workflow is released as soon as its own dependencies finish, never at a stage boundary, so one slow workflow does not hold up unrelated ones. A workflow that waits for a slot is re-queued when a slot frees, instead of the queue being rescanned on a timer.
- **`store/postgres/`** implements one store interface per aggregate (builds, workflows, runners, queues), each declared where the engine uses it. A single store interface for everything, or one flat package of queries, grows past what anyone can read.
- **`protocol/`** is the only thing the engine and the runner share: the messages, defined in protos, and one Go interface that both the transports and the tests implement. The runner always opens the connection. The calls are:
  - register and declare: the runner's name, labels and capacity;
  - next: a long poll that returns a workflow whose labels match;
  - accept, guarded by the version column;
  - update: step states and the measures a step reports (such as a session's tokens per model), with the lease renewed; every call carries the `traceparent` of the span it belongs to; the response carries a cancellation when there is one, so no separate channel is needed;
  - log: by offset, acknowledged, with a final "no more", so a resend is safe;
  - done.
- **`transport/inproc/`** implements the protocol in memory, for the local mode and the end-to-end tests. **`transport/connect/`** implements it over HTTP with connect-rpc, which crosses proxies and keeps the protos as the contract's documentation.
- **`api/`** serves clients, apart from the runner protocol.
- **`source/github/`** turns what happens on GitHub into events. Other sources (cron, a manual start) are adapters of their own.
- **`runner/`** runs one goroutine per unit of capacity, each looping on next and then running what it got. It renews the lease, stops a workflow the engine cancels, and reports step states and logs. Logs pass through a chain of `io.Writer`s (secret redaction, timestamps, chunks), so the executor never knows about the transport.
- **`runner/executor/`** runs one workflow through fixed phases (policy, environment, checkout, steps, artifacts, exit), with hooks from the runner, the repository and a plugin. Pre-hooks run in that order and post-hooks in reverse, and the first `checkout` hook found replaces the default checkout. The runner hands the executor a typed spec, not a long list of environment variables.
- **`runner/backend/`** is the interface for running a step: set up, run, destroy. `local` runs on the machine and `docker` in a container; `dummy` fakes steps from their settings (exit code, delay), so end-to-end tests need no docker.

### Boundaries

Inside the module, imports point inward, checked by `depguard` as crew's layers are:

- `spec`, `model` and `scheduler` import nothing with I/O.
- `runner` imports `protocol`, never `engine`, `scheduler` or `store`.
- `engine` imports no transport, backend or source.
- Only `telemetry` imports OpenTelemetry, and only `main` sets up its SDK.
- crew actions and crew are two repositories, and neither imports the other: no `depguard` rule is needed between them.

### Day one

`spec`, `spec/dag`, `model`, `scheduler`, `engine`, `store/postgres`, `protocol`, `transport/inproc`, `runner`, `runner/executor`, `runner/backend/local`, `runner/backend/dummy` and `telemetry`.
`transport/connect`, `runner/backend/docker`, `api` and `source/github` come when something uses them.

### Testing

- `scheduler` and `spec`: table tests, with no database and no clock.
- `store/postgres`: against a real Postgres, one database per test from a migrated template.
- `runner`: against a fake engine that serves the real protocol, not a mocked client, so requests really round-trip.
- End to end: a real engine and runner in one process, over `transport/inproc`, with the `dummy` backend: routing, depends_on, queue limits, cancellation, an expired lease, a restart.

## Measurement

Everything the engine runs is measured, with OpenTelemetry: traces for each execution, metrics for the aggregates.
No slice is done until the time and the tokens of the work it adds can be read from its spans and metrics.
crew 0.1.4 already measures its own work as a tree of spans in SQLite (`internal/crew/statistic.go`, `internal/adapter/sqlite/migrations/`); the engine keeps the idea and moves it to OpenTelemetry, and if the design has to change on the way, it changes.

### Spans

- **Everything that runs has a span, and so does every wait.** A build, a workflow, each attempt of it, each wait (for dependencies, for a slot, for a runner), each hook, each step, each session.
- **The span travels in the `context.Context`.** Starting a span reads its parent from the `ctx`; with no parent, it starts a root. A build from a GitHub event, a cron or a person starts the same way, so the tree does not depend on where the work came from.
- **Across a process or the network, the span's id travels as a W3C `traceparent`:** in the protocol, when the engine hands a workflow to a runner, and in the `TRACEPARENT` environment variable for a child process, so a command run inside a step attaches its measures to the right span.
- **Pure packages open no span.** `spec` and `scheduler` take no `ctx` and do no I/O. Spans start where the work runs: in `engine`, `runner` and `executor`.
- **Every span is kept.** Sampling is always on in the engine and the runner, since crew's own exporter needs every span; if a backend gets too much, the sampling happens after, in a Collector.
- **An open span is not exported.** OpenTelemetry exports a span when it ends, so a process killed midway loses the span it had open. The state of a build or a workflow that is running stays in the engine's tables, which already hold when it started; telemetry records what ended. One does not replace the other.

### Specific, not generic

The spans and metrics are crew's own, easy to identify, and crew sets them itself.
They follow no generic convention, and nothing depends on automatic instrumentation.

- **One package names them all:** `internal/telemetry`. It holds every span name, attribute key and metric instrument as constants, and typed helpers such as `telemetry.StartWorkflow(ctx, workflow)` or `telemetry.RecordTokens(ctx, model, tokens)`. No other package spells a name or imports OpenTelemetry; `depguard` checks it.
- **Every name starts with `crew.`,** so a span or a metric is crew's at a glance in any backend.
- **Span names are fixed and few;** what varies goes in attributes.
- **Metrics never carry an id** (a build, an issue, a workflow run): only bounded values such as the repository, the queue, the workflow's name, the model. Ids live on spans.

A first catalog, for the brainstorm to finish:

| Span | Attributes |
|---|---|
| `crew.build` | `crew.repository`, `crew.build.id`, `crew.build.trigger` (the event kind), `crew.outcome` |
| `crew.workflow` | `crew.workflow.name`, `crew.queue`, `crew.outcome` |
| `crew.workflow.attempt` | `crew.attempt`, `crew.runner`, `crew.outcome` |
| `crew.wait` | `crew.wait.for` (`dependencies`, `slot`, `runner`), `crew.queue` |
| `crew.hook` | `crew.hook.phase`, `crew.hook.origin` (`runner`, `repository`, `plugin`), `crew.outcome` |
| `crew.step` | `crew.step.name`, `crew.step.exit_status`, `crew.outcome` |
| `crew.session` | `crew.session.harness`, `crew.session.turns`, `crew.prompt.hash`, and one `crew.tokens` event per model, with `crew.model` and its input, output, cache read and cache write tokens |

| Metric | Kind | Attributes |
|---|---|---|
| `crew.tokens` | counter, tokens | `crew.model`, `crew.tokens.type` (`input`, `output`, `cache_read`, `cache_write`), `crew.repository`, `crew.workflow.name` |
| `crew.workflow.duration` | histogram, seconds | `crew.repository`, `crew.workflow.name`, `crew.queue`, `crew.outcome` |
| `crew.wait.duration` | histogram, seconds | `crew.wait.for`, `crew.queue`, `crew.repository` |
| `crew.queue.waiting` | gauge | `crew.queue` |
| `crew.queue.running` | gauge | `crew.queue` |
| `crew.runner.busy` | gauge | `crew.runner` |
| `crew.builds` | counter | `crew.repository`, `crew.build.trigger`, `crew.outcome` |

### Where the data goes

- **crew collects its own data.** A span exporter of crew's writes every finished span to the engine's Postgres, where crew and its users can query it, such as tokens per issue or time per workflow, the way crew's statistics are read today. A tracing backend is not built to answer those questions.
- **Anything else is optional and standard:** the SDK takes several exporters at once, so the same spans and metrics can also go out over OTLP to a Collector or a backend, chosen through OpenTelemetry's environment variables (`OTEL_TRACES_EXPORTER`, `OTEL_EXPORTER_OTLP_ENDPOINT`). Locally, a Jaeger container in the same docker compose as Postgres shows the traces.
- **Logs go through `log/slog`,** bridged to OpenTelemetry, so each log line carries the trace and span it happened in.
- **Only `main` sets up the SDK.** Everything else uses the API through `telemetry`; without an SDK, the API does nothing, so tests and a run with telemetry off cost nothing.

### What each slice measures

| Slice | Spans and metrics it adds |
|---|---|
| 0, Skeleton | the `telemetry` package, crew's span exporter to Postgres; `crew.build`, `crew.workflow`, `crew.workflow.attempt`, `crew.step`; `crew.workflow.duration`, `crew.builds` |
| 1, Hooks | `crew.hook` |
| 2, Workflows and `depends_on` | `crew.wait` for dependencies; skipped workflows as an outcome |
| 3, Agent session | `crew.session` and its `crew.tokens` events, reported by the runner; `crew.tokens` |
| 4, Runners and labels | `crew.runner` on attempts; `crew.wait` for a runner; `crew.runner.busy` |
| 5, Queues | `crew.wait` for a slot; `crew.wait.duration`, `crew.queue.waiting`, `crew.queue.running` |
| 6, Events | `crew.build.trigger`; the time from the event to the build |
| 7, Multi-repo and priority | `crew.repository` everywhere; waits by priority |
| 8, Containers | image pull and container start as spans of their own, apart from the step |
| 9, Remote runner | `traceparent` across the network; which clock each time comes from |
| 10, API and clients | spans of the API's calls |
| 11, Service mode | OTLP export set up for a deployment; the spans of several engines in one database |

### Still to decide

- Whether crew's span exporter and today's SQLite statistics meet: crew could later record through the same `telemetry` package, or keep its own store.
- How a long session reports its tokens before it ends, if anyone needs to see them live.
- The OpenTelemetry Go version and which exporters ship in the binary, with the libraries' review.

## Set aside

- **The step as the unit.** Each step would go to the queue on its own and could land on another runner, with no shared disk. A session and the scripts that check it need the same workspace, and the checkout would repeat at every step. A step that should be scheduled on its own goes in a workflow of its own.
- **A queue that is only a runner tag.** Such a tag picks a pool of runners but limits nothing. The queue is a policy of whoever writes the workflows, and the capacity is a resource of whoever runs the runner: they are two limits.
- **A wait running on a runner.** A step that only waits for another build holds a slot without working. A wait, if there is one, lives in the engine and holds no runner.

## Later

- The kind of priority: strict, weighted or aging; and whether a workflow can set its own `priority` (only among workflows of the same repository).
- Dependencies across repositories: a workflow in A that waits for one in B, which belongs to another build.
- Where the repository's hooks run: on the machine, or inside the container, so they do not break the isolation.
- The plugin model: a container image that its `settings` configure, as environment variables, or a bundle of hooks that runs at the runner's phases and can replace the checkout or a step's commands.
- What `when: status` looks at within a workflow, and between workflows.
- What happens when a second event arrives for something already running: queue it, cancel the earlier one, or ignore it.
- Where events come from: webhooks, polling for events, cron, a manual start.
- The database tests on macOS: GitHub's macOS runners have no Docker, so CI's Postgres service container only exists on Linux. Skip them there, or install Postgres in the macOS job.
- Each library, tool and version named here (pgx, sqlc or hand-written SQL, connect-rpc, the migration tool, the Postgres version): the brainstorm reviews them.
- Before copying any code from another project, check its license: some open projects keep parts under non-commercial terms.

## Next: a build's lifecycle

These questions belong to slice 0, the skeleton (see [Breaking it down](#breaking-it-down)).

- How an event enters the engine.
- How a build and its workflows are born: reading the files, applying the `when`s, linking the `depends_on`s.
- A workflow's states, from created to success or failure: waiting for dependencies, waiting for a slot, waiting for a runner, handed out, running, finished.
- What happens to the dependents of a workflow that failed.
- What the engine keeps to survive a restart. With the queue in Postgres, the builds, the queue, the slots and the leases are already rows; what is left is how an engine that starts resumes its loops and reaps the expired leases.

## Breaking it down

The whole engine is too large for one brainstorm.
It breaks into vertical slices: each one adds a capability across every part it touches (spec, scheduler, store, protocol, runner) and ends in something that runs.
Slicing by component instead (one brainstorm for the scheduler, one for the store) delivers nothing that runs alone, and decisions that cross components, like a workflow's states, get made several times in different ways.

Each slice stays under the size `/cw-split-brainstorm` splits at (10,000 characters or 12 requirements), so refinement does not have to split it again.
The dependencies below are a guide for refinement, which decides and records the links between the issues.

### Slice -1: the architecture document

Before any code, a document of the high-level architecture and the folder layout, for every slice to follow: `docs/architecture.md` in crew actions' repository, apart from this exploration, which stays in crew's.
This exploration is its starting point; the document is the curated version of what is decided.

It fixes structure, not behaviour: who owns what, who talks to whom, the names, the folders and the import rules.
The slices fix behaviour: what happens when a workflow fails, how a queue frees a slot.
Kept to that line, the document does not speculate: it does not say how the scheduler orders, only that the scheduler is pure, that only the engine calls it and that the runner never imports it.

What it holds:

1. **Purpose and non-goals:** an engine alongside crew, with no migration of crew now.
2. **A glossary:** build, workflow, step, runner, queue, lease, label. Every slice uses these words, and confusions start there.
3. **The hierarchy and the boundary:** the engine hands out, the runner executes.
4. **The folder tree,** one line of responsibility per package, and the import rules `depguard` checks.
5. **The protocol at the level of its calls:** names and purpose, not fields.
6. **The technical choices,** each with its version: Postgres, the database library, the migration tool, the transport. This is where the review of the libraries and versions happens.
7. **The milestones** (below), and which slice belongs to which.
8. **The telemetry catalog:** every span, attribute and metric, the `telemetry` package that alone names them, and how the span travels in the `ctx`, the protocol and the environment (see [Measurement](#measurement)).
9. **A decision log:** what was decided, in which slice, and why. A slice that changes something records the change with its reason instead of only rewriting the text.

How it stays true:

- **Every brainstorm starts by reading the document and ends by saying what changes in it.** A slice that changes the architecture shows it as a revised decision, with its reason, not hidden among its requirements.
- **Every delivery's pull request updates the document in the same pull request,** as crew's repository already requires for its README.
- **The import rules become `depguard` rules in slice 0,** when the first packages exist, so the document's most important part is checked by the linter.
- **Every brainstorm says what its slice measures, and its delivery proves it:** a feature whose time and tokens cannot be read from its spans is not done.
- **The new repository's `AGENTS.md` points at the document,** since that is how coding agents reach it.

In crew's flow, slice -1 is an issue like the others: a brainstorm, then a pull request with the document and no code. It also sets up the new repository: its module, CI and the files crew's repository has as a template.

### The slices

| # | Slice | What it delivers | Decisions it forces | Needs |
|---|---|---|---|---|
| -1 | **Architecture document** | the reference above | structure, names, folders, import rules, technical choices and versions | — |
| 0 | **Skeleton** | `crew actions run` with one file: one repository, one workflow, steps in sequence on the `local` backend, engine and runner in one process over `inproc`, the queue in Postgres with claim, lease and done, docker compose | the lifecycle and states of builds, workflows and steps; the base schema, with organization and repository; the minimal protocol | -1 |
| 1 | **Hooks** | the executor's phases (policy, environment, checkout, steps, artifacts, exit), the three levels of hooks (runner, repository, plugin), pre-hooks in order and post-hooks in reverse, the replaceable `checkout` | how a hook passes what it exports to the next phases | 0 |
| 2 | **Workflows and `depends_on`** | a build as a DAG, cycles rejected, `waiting` to `pending`, skipping what a failure rules out | what `when: status` looks at | 0 |
| 3 | **A coding-agent session as a step** | a step that runs a Claude session, in the worktree a `checkout` hook makes, as the bot an `environment` hook sets up, with the prompt and last message as artifacts | the plugin model | 1 |
| 4 | **Runners and labels** | several runners, capacity, the label match, runner registration | — | 0 |
| 5 | **Queues with a limit** | named queues in the engine's config, the limit checked in the claim's transaction, re-queued when a slot frees | — | 0 |
| 6 | **Events** | `source/github`, `when: event`, builds that start without a person | polling or webhooks; a second event for something already running | 2 |
| 7 | **Multi-repo and priority** | several repositories on one engine, the per-project priority in the ranking | the kind of priority | 5, 6 |
| 8 | **Containers** | the `docker` backend, the mounted workspace, a plugin as an image | where the repository's hooks run: on the machine, or inside the container | 1 |
| 9 | **Remote runner** | `transport/connect`, registration tokens | how runners authenticate | 4 |
| 10 | **API and clients** | the `api` package, the CLI and the TUI talking to the engine through it | — | 0 |
| 11 | **Service mode** | encrypted secrets, identity per repository, logs and artifacts off the disk, several engines side by side | — | 9, 10 |

Hooks come right after the skeleton because they are what makes a crew flow work:

- **`checkout`** is the worktree: on the right branch, reused when an issue comes back, not a plain clone.
- **`environment`** is the bot's identity: its GitHub token and the git setup of the co-author hook. Without it a session runs as whoever runs crew.
- **`pre-workflow`** is the policy, such as "this runner runs no session on an issue a code owner did not open".
- **`pre-exit`** cleans up what a session leaves behind.

Without hooks, the agent session would have to carry the worktree and the identity as settings of its plugin, and move them into hooks later: the same work done twice.
While `local` is the only backend, every hook runs on the machine anyway, so where the repository's hooks run is not a question yet; it waits for the containers.

The agent session (slice 3) comes early on purpose.
crew exists to run coding-agent sessions, and a session is not a CI command: it lasts hours, needs the machine's credentials and ends in a text someone judges.
If it does not fit the model of workflows and steps, finding out at the fourth slice costs far less than at the tenth.

### Milestones

Nothing has to be finished for the engine to be usable.
Each milestone says what can be used once its slices are done, and under which limits.

| Milestone | After slices | Usable for | Limits |
|---|---|---|---|
| **M0: runs a file by hand** | -1, 0 | running a workflow of `commands` steps, with everything recorded in Postgres: states, logs, a runner that dies and the workflow that goes back to the queue | a manual start, one repository, one machine, one runner, the `local` backend, no hooks, no dependencies |
| **M1: runs an agent flow by hand** | 1, 2, 3 | crew's real work, started by hand: a Claude session in its worktree, as its bot, followed by the scripts that check it, in workflows with `depends_on`, such as `crew actions run development.yaml` with the issue passed as a variable | still manual, one machine, one repository |
| **M2: runs on its own in one repository** | 4, 5, 6 | the engine reacts to GitHub events, with several runners on the machine, labels and queues with a limit. From here a repository can move one flow at a time from crew to the actions engine, with crew running the rest | one machine, one repository, the `local` backend |
| **M3: several repositories on one machine** | 7 | one engine and the same runners serving several repositories, with per-project priority | one machine, the `local` backend |
| **M4: isolated** | 8 | steps in containers, and the repository's hooks where the isolation needs them | still one machine |
| **M5: several machines** | 9, 10 | runners on other machines, and the CLI and the TUI talking to a remote engine | simple runner authentication; no secrets kept by the engine |
| **M6: a service** | 11 | the engine as a service: encrypted secrets, identity per repository, logs off the disk, several engines side by side | — |

- **Events come before containers.** Without them nothing runs on its own, and running on its own is what makes the engine useful day to day. Containers add isolation and reproducibility, but the `local` backend already serves today's flows, which run on the machine.
- **M4 can move.** It needs only the skeleton and the hooks, so it can come before M2 or M3 if isolation matters more.

Each milestone has a demonstration that proves it closed.
For M0: run a file, kill the runner midway, and watch the workflow go back to the queue and finish.
For M2: open a pull request and watch the workflow start on its own, with the queue holding the second one.

On GitHub the milestones can become milestones, holding the issues of their slices, so progress toward the next one shows without effort.
