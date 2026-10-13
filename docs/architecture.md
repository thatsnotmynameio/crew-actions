# crew actions: architecture

The reference every slice follows: who owns what, who talks to whom, the names, the folders, the import rules and the technical choices.
It fixes structure, not behaviour.
It says that the scheduler is pure, that only the engine calls it and that the runner never imports it; how the scheduler orders work is a slice's to decide.

It holds what can be settled now.
What cannot is listed under [Open, and who decides it](#open-and-who-decides-it), with the slice that owns it.
The exploration in [`docs/exploration/crew-actions.md`](exploration/crew-actions.md) is where this started; it is no longer maintained, and where the two disagree, this document wins.

## How it stays true

- **Every brainstorm starts by reading this document and ends by saying what changes in it.** A slice that changes the architecture shows it as a revised decision, with its reason, not hidden among its requirements.
- **Every delivery's pull request updates this document in the same pull request.**
- **A change is a new entry in the [decision log](#9-decision-log),** naming the entry it supersedes. The text above the log is rewritten to match; the log keeps the history.
- **The import rules become `depguard` rules** in `.golangci.yml`, each citing in its `desc` the decision behind it, so the most important part of this document is checked by the linter.
- **Every slice says what it measures, and its delivery proves it:** a feature whose time and tokens cannot be read from its spans and metrics is not done.

---

## 1. Purpose and non-goals

crew actions is an actions engine built alongside [crew](https://github.com/thatsnotmynameio/crew), for crew's case: coding-agent sessions that last hours, need the machine's credentials and end in a text someone judges, next to the scripts that check them.
Workflows are written in YAML; runners ask the engine for work; the queue lives in Postgres.
From milestone M2 on, a repository can move one flow at a time from crew to the engine.

Non-goals:

- **No migration of crew now.** Today's crew keeps working, and this design does not start from its rules, routes or labels.
- **No verdicts, routes or states of crew's own.** A step and a workflow end in success or failure, and nothing else.
- **No telemetry storage or viewer.** The engine exports its data; where it goes is the OpenTelemetry Collector's job (D14).

---

## 2. Glossary

| Word | Meaning |
|---|---|
| **engine** | The service that receives events, creates builds, holds the queue, hands workflows to runners, receives their states, logs and results, and closes builds. It never runs a step. |
| **repository** | A source repository the engine knows. One engine serves several. |
| **event** | What starts a build: a GitHub event, a schedule or a person. |
| **pipeline** | The set of workflow files in a repository's `.crew/workflows/`: the definition. |
| **build** | One run of a pipeline, started by one event. |
| **workflow** | One file of the pipeline, and the queue's unit: it goes whole to one runner, and its steps share one workspace. `depends_on` orders workflows within a build. |
| **step** | A command, or a plugin, run inside a workflow's workspace, in order with the other steps. |
| **outcome** | How a step, a workflow or a build ends: success or failure. |
| **runner** | A process that asks the engine for work and runs it. It only opens outgoing connections. |
| **capacity** | How many workflows a runner runs at once. It belongs to whoever runs the runner. |
| **label** | A key and value on a runner, matched by a workflow's `labels`. Labels say *where* a workflow runs. |
| **queue** | A name with a limit, in the engine's configuration. A workflow names its queue. The queue says *how many* run at once, on any runner; it never picks a runner. |
| **slot** | One unit of a queue's limit. Taken when a workflow is handed out, freed when it ends or its lease expires. |
| **lease** | A runner's claim on a workflow it is running, renewed while it works. When a lease expires, the workflow goes back to the queue. |
| **hook** | What the runner does around a workflow, at fixed phases. Hooks come from the runner, the repository and a plugin. |
| **backend** | Where a step runs: `local` (on the machine), `docker` (in a container) or `dummy` (faked, for tests). |

---

## 3. Hierarchy and boundary

```text
engine (knows several repositories)
└── repository
    └── build (one run, started by an event)
        └── workflows   ← the queue's unit; depends_on between them; each goes whole to one runner
            └── steps   ← run in order on that runner, in the same workspace
```

**The engine hands out; the runner executes.**
The boundary holds even with one runner in the same process: the two talk only through the protocol (section 5), over `transport/inproc` in one process and `transport/connect` across machines.

Three seams exist from the first day, because they cost little now and a lot later:

1. **An API for clients (CLI, TUI, web), apart from the runner protocol.** Clients talk to the engine through it even in one process.
2. **Organization and repository on every row,** from the first migration.
3. **The engine's configuration behind an interface:** a file locally; the database or an API as a service.

What differs between the local mode and a service (database, transport, event source, identity, secrets, logs) is an interface, never a second program.

---

## 4. Folder tree and import rules

```text
crew-actions/
  go.mod                   # github.com/thatsnotmynameio/crew-actions
  cmd/crew-actions/        # the binary: flags, environment, the OpenTelemetry SDK set up, wiring
  internal/
    spec/                  # YAML: parse, lint, compile into a neutral form
    spec/dag/              # the depends_on graph, with cycle detection
    model/                 # Build, Workflow, Step; states and outcomes as types, not strings
    scheduler/             # pure: from a snapshot of the candidates, picks one
    engine/                # the use cases; owns the transactions
    store/postgres/        # the store interfaces' Postgres implementation, the migrations, the queries
    protocol/              # the engine–runner contract: the protos and one Go interface
    transport/inproc/      # the protocol in memory: engine and runner in one process
    transport/connect/     # the protocol over HTTP: engine and runner on different machines
    api/                   # the API for clients, apart from the protocol
    source/github/         # turns what happens on GitHub into events
    runner/                # the poller and its capacity, the lease, cancellation, reporting, logs
    runner/executor/       # the phases and hooks of one workflow
    runner/backend/        # the Backend interface and its implementations: local, docker, dummy
    telemetry/             # every span, attribute and metric name, and the typed helpers
    proc/                  # starting child processes (from slice 0, see D16)
```

Each package in one line:

- **`spec`** compiles the YAML before anything runs, so the engine validates a whole build at once and the runner receives something already resolved. **`spec/dag`** rejects a cycle when the files are read.
- **`model`** types every state and outcome.
- **`scheduler`** takes a read-only snapshot and returns a choice, with no I/O and no `context.Context`. It is a chain of small stages (filters, then a ranking), each tested alone.
- **`engine`** loads the snapshot, asks the scheduler and writes the choice in the same transaction. It holds the use cases: create a build, hand out, accept, renew a lease, finish, release dependents.
- **`store/postgres`** implements one store interface per aggregate (builds, workflows, runners, queues), each declared where the engine uses it.
- **`protocol`** is the only thing the engine and the runner share.
- **`transport/inproc`** and **`transport/connect`** implement the protocol; the tests implement it too.
- **`api`** serves clients.
- **`source/github`** is one event source; other sources (a schedule, a manual start) are adapters of their own.
- **`runner`** runs one goroutine per unit of capacity, each looping on `next` and running what it got. Logs pass through a chain of `io.Writer`s, so the executor never knows the transport.
- **`runner/executor`** runs one workflow through fixed phases with the hooks of the runner, the repository and a plugin.
- **`runner/backend`** sets up, runs and destroys a step's environment.
- **`telemetry`** alone names spans, attributes and metrics, and alone imports OpenTelemetry.
- **`proc`** is the one way the binary starts a child process.

Import rules, checked by `depguard` (D7):

- `spec`, `spec/dag`, `model` and `scheduler` import nothing that does I/O.
- Only `engine` imports `scheduler`.
- `runner` and its subpackages import `protocol`, never `engine`, `scheduler` or `store`.
- `engine` imports no transport, backend or source.
- Only `store/postgres` imports pgx, goose and the code sqlc generates.
- Only `transport/connect` imports connect-go.
- Only `telemetry` imports OpenTelemetry, and only `cmd/crew-actions` sets up its SDK.
- crew actions and crew are two repositories, and neither imports the other.

`transport/connect`, `runner/backend/docker`, `api` and `source/github` come when a slice uses them; the rest arrive with slice 0.

---

## 5. The protocol

The runner always opens the connection.
Every call carries the W3C `traceparent` of the span it belongs to.
This section names the calls and their purpose; their fields belong to the slices that use them.

| Call | Purpose |
|---|---|
| **register** | The runner declares its name, labels and capacity. |
| **next** | A long poll that returns a workflow whose labels match the runner's. |
| **accept** | The runner takes the workflow it was handed. When two runners race for one workflow, the loser drops it. |
| **update** | Step states and the measures a step reports, such as a session's tokens per model. It renews the lease, and its response carries a cancellation when there is one, so no separate channel is needed. |
| **log** | Log chunks by offset, acknowledged, with a final "no more", so a resend is safe. |
| **done** | The workflow's outcome. |

---

## 6. Technical choices

Each choice names its major version only where the major is the choice.
Exact versions live in `go.mod`, the CI workflows and the compose file, and Dependabot moves them; this document does not repeat them (D15).

| Choice | What it does here | Why |
|---|---|---|
| **Go**, the version in `go.mod` | The language and toolchain. | crew's language; one binary. |
| **PostgreSQL 18** | The only database: the builds, the workflows and the queue. | One dialect lets the engine use what Postgres does well: `FOR UPDATE SKIP LOCKED`, LISTEN/NOTIFY, advisory locks. 19 is still in beta. |
| **pgx v5** (`github.com/jackc/pgx/v5`) | The driver and the connection pool. | The standard Postgres driver in Go, with native LISTEN/NOTIFY. |
| **sqlc** | Generates typed Go from plain SQL queries, checked against the schema. | Queries stay SQL; mistakes show at generation, not at run time. |
| **goose** (`github.com/pressly/goose/v3`) | Runs the migrations: plain `.sql` files embedded in the binary, at startup. | Widely used; reads `embed.FS`; holds a Postgres advisory lock while it migrates, so several engines can start at once. |
| **The compose file** (`compose.yaml`) | Starts PostgreSQL 18 for the store's tests, locally and in CI's Linux job; the tests read its URL from `CREW_ACTIONS_TEST_DATABASE_URL` (D19). | One server for every test run, reached the same way in both places, with no test-only dependency in `go.mod`. |
| **pgtestdb** | Gives each test its own database, created from a migrated template, on the server `CREW_ACTIONS_TEST_DATABASE_URL` names. | Store tests run in parallel, each on a clean database, without migrating each time. |
| **connect-go v2** (`connectrpc.com/connect/v2`) | The protocol over HTTP for `transport/connect`. | Works over HTTP/1.1 and HTTP/2, so it crosses proxies; the protos stay the contract. |
| **protobuf-go** and **buf** | The protocol's messages, and the tool that lints and generates them. | protobuf-go is the official runtime; buf replaces protoc. |
| **OpenTelemetry Go**, with **autoexport** and the **otelslog** bridge | Traces, metrics and logs, exported over OTLP. | The standard; exporters chosen by the standard `OTEL_*` variables. |
| **`log/slog`** | Logs, bridged to OpenTelemetry. | The standard library's logger; each line carries its trace and span. |

What the choices bring with them:

- **sqlc covers ordinary queries, not everything.** LISTEN, the advisory locks around the engine's loops and the claim loop are written by hand over pgx. Session advisory locks and LISTEN each hold one dedicated connection for as long as they last. sqlc's own build needs cgo, so it runs from its release binary rather than the `tool` directive of `go.mod`, and the generated code is committed.
- **buf is not a `go tool`.** Its documentation advises against it; it is installed at a pinned version. The protoc plugins (`protoc-gen-go`, `protoc-gen-connect-go`) do go through the `tool` directive.
- **otelslog and the OTLP log exporter are still experimental** upstream, unlike the trace and metric SDKs. A Dependabot bump can change their API.
- **The store's tests need `CREW_ACTIONS_TEST_DATABASE_URL`.** Without it they skip and name the variable. Only CI's Linux job sets it, since GitHub's macOS runners have no Docker to run the compose file: the tests run in the Linux job and skip in the macOS one.
- **The Postgres 18 image keeps its data under `/var/lib/postgresql`,** not `/var/lib/postgresql/data` as before; the compose file mounts the tests' data there.

Not taken:

- **A second SQL dialect, or an ORM:** generated DDL per dialect, or a layer that hides Postgres's features.
- **lib/pq:** `database/sql` has no API for notifications.
- **grpc-go as the transport:** it needs HTTP/2 end to end, which proxies often break. **twirp:** no release since 2022.
- **Automatic instrumentation (otelpgx, otelconnect):** the engine names and opens its own spans (D13).
- **testcontainers-go:** about 50 test-only dependencies in `go.mod`, two of them under licenses `dependency-review` rejects (D19).

---

## 7. Milestones and slices

Nothing has to be finished for the engine to be usable: each milestone says what can be used once its slices are done, and under which limits.

| Milestone | Slices | Usable for | Limits |
|---|---|---|---|
| **M0: runs a file by hand** | -1, 0 | running a workflow of `commands` steps, with everything recorded in Postgres, and a runner that dies sending its workflow back to the queue | a manual start, one repository, one machine, one runner, the `local` backend, no hooks, no dependencies |
| **M1: runs an agent flow by hand** | 1, 2, 3 | a Claude session in its worktree, as its bot, followed by the scripts that check it, in workflows with `depends_on` | still manual, one machine, one repository |
| **M2: runs on its own in one repository** | 4, 5, 6 | GitHub events start builds, with several runners, labels and queues with a limit; a repository can start moving flows from crew | one machine, one repository, the `local` backend |
| **M3: several repositories on one machine** | 7 | one engine and the same runners for several repositories, with per-project priority | one machine, the `local` backend |
| **M4: isolated** | 8 | steps in containers | still one machine |
| **M5: several machines** | 9, 10 | remote runners; the CLI and the TUI against a remote engine | simple runner authentication; no secrets kept by the engine |
| **M6: a service** | 11 | encrypted secrets, identity per repository, logs off the disk, several engines side by side | — |

M4 needs only slices 0 and 1, so it can move ahead of M2 or M3 if isolation matters more.
Each milestone closes with a demonstration; for M0: run a file, kill the runner midway, and watch the workflow go back to the queue and finish.

| Slice | Delivers | Needs | Issue |
|---|---|---|---|
| -1 | this document | — | #3 |
| 0 | `crew actions run` with one file: one workflow, steps in sequence on `local`, engine and runner in one process, the queue in Postgres | -1 | #4 |
| 1 | hooks: the executor's phases, the three levels, the replaceable `checkout` | 0 | #5 |
| 2 | workflows and `depends_on`: a build as a DAG | 0 | #6 |
| 3 | a coding-agent session as a step | 1 | #7 |
| 4 | runners and labels | 0 | #8 |
| 5 | queues with a limit | 0 | #9 |
| 6 | events | 2 | #10 |
| 7 | multi-repo and priority | 5, 6 | #11 |
| 8 | containers | 1 | #12 |
| 9 | the remote runner | 4 | #13 |
| 10 | the API and its clients | 0 | #14 |
| 11 | service mode | 9, 10 | #15 |

The slices' issues live in this repository, as sub-issues of #2, and move through crew's labels.

---

## 8. Telemetry

Everything the engine runs is measured with OpenTelemetry: traces for each execution, metrics for the aggregates.

### Rules

- **Everything that runs has a span, and so does every wait.**
- **One package names them all:** `internal/telemetry` holds every span name, attribute key and metric instrument as constants, with typed helpers such as `telemetry.StartWorkflow(ctx, workflow)`. No other package spells a name or imports OpenTelemetry.
- **Every name starts with `crew.`** Span names are fixed and few; what varies goes in attributes.
- **Metrics never carry an id** (a build, an issue, a workflow run), only bounded values such as the repository, the queue, the workflow's name or the model. Ids live on spans.
- **Pure packages open no span.** `spec` and `scheduler` take no `ctx`. Spans start where the work runs: `engine`, `runner`, `executor`.
- **No sampling in the engine or the runner.** Every span is kept; sampling, if a backend needs it, happens in a Collector.
- **An open span is not exported.** The state of a running build or workflow lives in the engine's tables; telemetry records what ended.

### How the span travels

- **In the process,** in the `context.Context`: starting a span reads its parent from the `ctx`, and with none it starts a root. A build from a GitHub event, a schedule or a person starts the same way.
- **Between the engine and the runner,** as a W3C `traceparent` on every protocol call (section 5).
- **Into a child process,** as the `TRACEPARENT` environment variable, so a command run inside a step attaches its measures to the right span.

### Export

The engine only exports.
With no `OTEL_*` variables set, nothing is exported; with them, spans, metrics and logs go out over OTLP to whatever the variables name, usually a Collector, which decides where the data goes.
The engine keeps no telemetry of its own and ships no viewer (D14).
Only `cmd/crew-actions` sets up the SDK; without it, the API does nothing, so tests and a run with telemetry off cost nothing.

### Catalog

Each entry names the slice that adds it.
A slice's brainstorm finishes its own entries, and its delivery updates this catalog.

| Span | Attributes | Slice |
|---|---|---|
| `crew.build` | `crew.repository`, `crew.build.id`, `crew.outcome`; `crew.build.trigger` (the event kind) from slice 6 | 0 |
| `crew.workflow` | `crew.workflow.name`, `crew.queue`, `crew.outcome` | 0 |
| `crew.workflow.attempt` | `crew.attempt`, `crew.outcome`; `crew.runner` from slice 4 | 0 |
| `crew.step` | `crew.step.name`, `crew.step.exit_status`, `crew.outcome` | 0 |
| `crew.hook` | `crew.hook.phase`, `crew.hook.origin` (`runner`, `repository`, `plugin`), `crew.outcome` | 1 |
| `crew.wait` | `crew.wait.for` (`dependencies` from slice 2, `runner` from slice 4, `slot` from slice 5), `crew.queue` | 2 |
| `crew.session` | `crew.session.harness`, `crew.session.turns`, `crew.prompt.hash`, and one `crew.tokens` event per model with `crew.model` and its input, output, cache read and cache write tokens | 3 |

| Metric | Kind | Attributes | Slice |
|---|---|---|---|
| `crew.builds` | counter | `crew.repository`, `crew.outcome`; `crew.build.trigger` from slice 6 | 0 |
| `crew.workflow.duration` | histogram, seconds | `crew.repository`, `crew.workflow.name`, `crew.queue`, `crew.outcome` | 0 |
| `crew.tokens` | counter, tokens | `crew.model`, `crew.tokens.type` (`input`, `output`, `cache_read`, `cache_write`), `crew.repository`, `crew.workflow.name` | 3 |
| `crew.runner.busy` | gauge | `crew.runner` | 4 |
| `crew.wait.duration` | histogram, seconds | `crew.wait.for`, `crew.queue`, `crew.repository` | 5 |
| `crew.queue.waiting` | gauge | `crew.queue` | 5 |
| `crew.queue.running` | gauge | `crew.queue` | 5 |

Later slices add what they measure when they are brainstormed: image pull and container start (8), which clock each time comes from across machines (9), the API's calls (10), several engines in one database (11).

---

## Open, and who decides it

| Question | Slice |
|---|---|
| A workflow's states from created to finished; the base schema; the protocol's fields | 0 |
| How an engine that starts resumes its loops and reaps expired leases | 0 |
| How a hook passes what it exports to the next phases | 1 |
| What happens to the dependents of a failed workflow; what `when: status` looks at | 2 |
| The plugin model; how a long session reports its tokens before it ends | 3 |
| Polling or webhooks; what a second event does to a build already running | 6 |
| The kind of priority: strict, weighted or aging | 7 |
| Where the repository's hooks run: on the machine or in the container | 8 |
| How runners authenticate | 9 |
| Dependencies across repositories | later |

---

## 9. Decision log

Each entry says what was decided, in which slice, and why.
A later change adds a new entry that names the one it supersedes.

- D1. **A repository and a Go module of its own** (slice -1). `thatsnotmynameio/crew-actions`, module `github.com/thatsnotmynameio/crew-actions`. Its dependencies, releases, CI and issues stay its own, and the isolation from crew is the repository's: neither imports the other.
- D2. **The engine hands out; the runner executes** (slice -1). The boundary holds in one process, so the local mode is the service in one process, not a second program.
- D3. **The workflow is the queue's unit** (slice -1). A session and the scripts that check it need the same workspace; a step scheduled alone would lose it and repeat the checkout. A step that must be scheduled alone goes in a workflow of its own.
- D4. **Outcomes are success or failure only** (slice -1). Reacting to a failure is an ordinary step or workflow with `when: status: failure`.
- D5. **`queue` and `labels` are separate** (slice -1). Labels pick where; the queue limits how many. They are two limits owned by two people: whoever writes the workflows and whoever runs the runner.
- D6. **Postgres only, and the database is the queue** (slice -1). Claiming with `SKIP LOCKED`, the queue's limit checked in the claiming transaction, a lease, a version column, LISTEN/NOTIFY and advisory locks: a restart loses nothing, and several engines run side by side with no leader election.
- D7. **Packages are layered, and imports point inward** (slice -1). The rules in section 4, checked by `depguard`, so the architecture breaks the build rather than relying on review.
- D8. **pgx v5** (slice -1). The standard driver, with native LISTEN/NOTIFY; lib/pq has no notifications under `database/sql`.
- D9. **sqlc for queries; LISTEN, locks and the claim loop by hand over pgx** (slice -1). Typed code from plain SQL, checked against the schema; sqlc does not cover connections held for a lock or a LISTEN.
- D10. **goose for migrations** (slice -1). Widely used, reads `embed.FS`, and migrates under a Postgres advisory lock; a runner of our own would be code to maintain.
- D11. **testcontainers-go and pgtestdb for the store's tests** (slice -1). Tests start their own Postgres and get a database each from a migrated template; chosen over a compose file and a CI service container, which need a URL set up in each place.
- D12. **connect-go v2, protobuf-go and buf** (slice -1). Connect crosses proxies over HTTP/1.1; v2 is the current major line, so nothing migrates later.
- D13. **Telemetry names are crew's own, in one package** (slice -1). `crew.` names, set by `internal/telemetry`, with no automatic instrumentation; a generic convention or a library's spans would make crew's work hard to find and spread OpenTelemetry through the code.
- D14. **The engine only exports telemetry, over OTLP, when `OTEL_*` asks for it** (slice -1). Storing and showing the data is the Collector's and the backend's job. Supersedes the exploration's span exporter to Postgres and its local Jaeger.
- D15. **This document pins no versions** (slice -1). It names each choice and its major line where that is the choice; `go.mod`, CI and the compose file hold the versions, and Dependabot moves them.
- D16. **Child processes through a copy of crew's `internal/proc`** (slice -1). Another repository cannot import crew's `internal/`, and a module shared by both would tie their releases. The copy arrives with slice 0 and keeps crew's copyright notice, since the two licenses name different holders.
- D17. **A `crew-actions` binary, and `crew actions …` as a dispatch** (slice -1). crew runs `crew-actions` from the `PATH` with the rest of the arguments, so neither project requires the other. The dispatch is a small change in crew, made when there is something to dispatch to.
- D18. **One architecture document, with its decision log inside** (slice -1). One file is what a person or an agent landing in the repository reads; the exploration stays as history and is no longer maintained.
- D19. **The store's tests read their database URL from `CREW_ACTIONS_TEST_DATABASE_URL`** (slice 0). They start no container themselves: `compose.yaml` starts the server, locally and in CI's Linux job. When the variable is unset or empty, they skip with a message naming it; when it is set and its server cannot be reached, the run fails rather than skips, so a lost database never passes silently. testcontainers-go took `go.mod` from 16 indirect dependencies to about 70, all for tests, and `dependency-review` rejected the licenses of two of them. Supersedes D11.
