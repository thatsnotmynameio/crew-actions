---
title: A shared test template database is reused only once it is marked finished, never because its name exists
date: 2026-10-13
last_updated: 2026-10-14
category: best-practices
module: internal/store/postgres/internal/testdb
problem_type: best_practice
component: database
severity: medium
applies_when:
  - Tests clone a migrated template database with CREATE DATABASE ... TEMPLATE
  - The template outlives the test binary that made it, such as on the compose test server every worktree shares
  - Code decides whether to build the template by looking it up in pg_database
  - A change adds a step after the migrations that the template must have, or changes how the template is made or found
root_cause: data_integrity
resolution_type: code_fix
tags: [postgres, template-database, test-database, datistemplate, advisory-lock, migrations, testdb, crash-safety]
---

# A shared test template database is reused only once it is marked finished, never because its name exists

## Context

`testdb.New` gives each test a database of its own. It copies a template the store's migrations were applied to once. The template is named after a hash of the migrations (`templateName`, `internal/store/postgres/internal/testdb/testdb.go:148`), and it is built under a Postgres advisory lock so that test binaries running side by side build it once and share it.

#70 added this code as `postgrestest`, for tests outside the store. #104 moved it to `internal/store/postgres/internal/testdb`, which takes the migrations and the function that applies them instead of importing the store. `postgrestest.New` now wraps it with the store's migrations, and the store's own in-package tests call `testdb.New` directly, so every store test also runs on a copy of the template. #70's plan (`docs/plans/2026-10-13-1455-issue-70-plan.md`) still says, in KTD16, that the store's tests empty the shared database's tables, and names `newStore`'s truncate list. Since #104 neither holds: no test empties a shared database, and the store's tests have no `TestMain` that migrates one.

The plan for #70 (`docs/plans/2026-10-13-1455-issue-70-plan.md`, U3) says the template is "created only when absent", and the first version did exactly that. It looked the name up in `pg_database` and, when a row was there, used it. That plan is not updated after it ships, so it still describes the rejected design.

Code review found the gap. Building the template takes several steps: `CREATE DATABASE`, then the migrations, over a connection pool of their own (the `Migrate` function the caller passes, which `migrateTemplate` runs, `testdb.go:135`). A test binary can die between them: Ctrl-C, the panic `go test -timeout` raises, a cancelled CI job, or a failed `DROP` after a failed migrate. The database then stays on the server under the hash name, half migrated. Every later `New`, in every worktree, finds the name, skips the build and copies the broken template. `New` then fails on its `UPDATE database_identity`, or the tests fail on missing tables. The compose server keeps its data on a tmpfs (`compose.yaml`), so the broken template lasts until someone drops it by hand or runs `docker compose down`, which stops the server for every worktree.

The hash name does not help here. It changes only when the migrations change, and a half-built template has the same migrations as a finished one.

## Guidance

Treat "a database of this name exists" as "a build was started", not "a build finished". Record that the build finished as its last step, and check for that record:

1. Under the advisory lock, read `datistemplate` for the name, not its existence.
2. Marked: use it.
3. Present but not marked: a build died here. `DROP DATABASE ... WITH (FORCE)` and build it again.
4. Absent: `CREATE DATABASE`, migrate, and only then `ALTER DATABASE ... WITH IS_TEMPLATE true`.

`ensureTemplate` (`testdb.go:96`) does this:

```go
var finished bool
err = conn.QueryRow(ctx, "SELECT datistemplate FROM pg_database WHERE datname = $1", name).Scan(&finished)
switch {
case err == nil && finished:
	return nil
case err == nil:
	if _, err := conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop unfinished template %s: %w", name, err)
	}
case !errors.Is(err, pgx.ErrNoRows):
	return fmt.Errorf("look the template up: %w", err)
}
// CREATE DATABASE, migrate, then:
if _, err := conn.Exec(ctx, "ALTER DATABASE "+name+" WITH IS_TEMPLATE true"); err != nil {
	return fmt.Errorf("mark database %s as a template: %w", name, err)
}
```

`datistemplate` is Postgres's own flag, so the marker needs no extra table and cannot be copied into the clones. A later step the template must have goes before the `ALTER`, never after it.

## Why This Matters

**Rejected: running migrate again on an existing template.** goose skips applied migrations, so re-running migrate looks like a cheap way to repair a half-built template. It breaks parallel tests. `New` copies the template after `ensureTemplate` has returned, and so after the advisory lock is released (`testdb.go:65-71`). Postgres refuses `CREATE DATABASE ... TEMPLATE` while any other session is connected to the source, with "source database ... is being accessed by other users". A binary re-running migrate holds connections to the template while other binaries copy it, and their copies fail. Only the binary that builds the template may connect to it, and only before it is marked.

**Rejected: building the template under a temporary name and renaming it when done.** The review offered this as an equal alternative. It also leaves only finished templates under the hash name, but it leaves orphans under random names that nothing cleans up. The `datistemplate` check finds and replaces the unfinished database itself.

**A marked template cannot be dropped as it is.** Postgres refuses `DROP DATABASE` on a database with `datistemplate` set. To remove an old template by hand, run `ALTER DATABASE <name> WITH IS_TEMPLATE false` first, as the cleanup of `templateDatabase`, which `TestEnsureTemplateRebuildsAnUnfinishedTemplate` uses, does (`internal/store/postgres/internal/testdb/testdb_test.go:94`). `ensureTemplate`'s own drop applies only to unmarked databases, so it does not hit this.

**Templates the first version built are rebuilt once.** It never marked its templates, so on a server that still holds one, the first run of the fixed code drops it and builds it again. The fix session saw this on the shared local server (session history).

**Coverage of the build path depends on the server's state.** `ensureTemplate`'s build branches run only when no finished template is there. A coverage run against a server that already holds one, or a profile `go test` replays from its cache, reports them as uncovered. Measure with `-count=1` against a fresh template (session history).

## When to Apply

- Changing `ensureTemplate`, `New` or `templateName`.
- Adding a step that prepares the template after its migrations, such as seed data. It goes before the `ALTER DATABASE ... WITH IS_TEMPLATE true`.
- Building any other shared resource that outlives the process that makes it and is found by name: a cache directory, a container image, a schema. Write the "finished" marker last, and treat an unmarked one as broken.

## Examples

`TestEnsureTemplateRebuildsAnUnfinishedTemplate` (`testdb_test.go:104`) creates an empty database under a template name of its own, as a binary that died while migrating would leave it. It then calls `ensureTemplate` and checks that the database is marked and has the `database_identity` table. It uses a random name so the template other tests copy is never touched.

Before the fix, the lookup was, with its error check left out:

```go
var exists bool
conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
if exists {
	return name, nil // a half-migrated template is returned as well
}
```

## Related

- `docs/solutions/test-failures/goose-concurrent-migrate-test-passes-without-the-lock.md`: the store's other lock around migrations, goose's session lock in `Migrate`, and how to prove a lock test fails without the lock.
- #70, which adds `postgrestest`. It is part 3 of #22.
- #104, which moves the template code to `internal/store/postgres/internal/testdb` so the store's own tests use it too.
- `docs/solutions/test-failures/waitgroup-barrier-across-parallel-subtests-hangs.md`: the test #104 added to show that two store tests no longer share a database, and why its barrier can hang.
