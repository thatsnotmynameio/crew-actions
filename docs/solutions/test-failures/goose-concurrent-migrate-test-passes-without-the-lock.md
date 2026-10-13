---
title: A concurrent Migrate test passes without goose's session lock on a fresh database
date: 2026-10-13
category: test-failures
module: internal/store/postgres
problem_type: test_failure
component: database
symptoms:
  - TestMigrateConcurrently passed 5 runs out of 5 with goose.WithSessionLocker removed
  - Two concurrent Migrate calls on an empty database both succeed with or without the lock
root_cause: concurrency
resolution_type: test_fix
severity: medium
tags: [goose, migrations, advisory-lock, session-locker, postgres, concurrency, mutation-testing]
---

# A concurrent Migrate test passes without goose's session lock on a fresh database

## Problem

`Store.Migrate` relies on goose's Postgres session locker so that several engines can start at once and apply the schema once (architecture D10). The test meant to prove this raced two `Migrate` calls on an empty database. It passed even with the lock removed, so it proved nothing.

## Symptoms

- With `goose.WithSessionLocker(locker)` removed from `newProvider` (`internal/store/postgres/migrate.go:48`), `TestMigrateConcurrently` still passed 5 runs out of 5.
- Both calls returned no error, and the migration was recorded once.

## What Didn't Work

- Racing two `Migrate` calls on a database no migration had run on. This is the obvious way to write the test, and it is the one that passes without the lock.

## Solution

Before the race, make goose create its version table with a call that takes no lock. Then race the two calls. `TestMigrateConcurrently` (`internal/store/postgres/migrate_test.go:196`) does this with `HasPending`, through the `provider` helper (`migrate_test.go:166`), which builds the same provider `Migrate` uses:

```go
// goose's version table exists, as it does for every migration after
// the first, so both calls find the migration pending at once. Creating
// it in the race would delay one call until the other is done.
if _, err := provider(t, stores[0]).HasPending(t.Context()); err != nil {
	t.Fatalf("HasPending() error = %v", err)
}
// ... then run both Migrate calls at once and check the migration was applied once.
```

Mutation check after the fix:

- Lock removed: the test fails 3 runs out of 3 with `partial migration error (type:sql,version:1): duplicate key value violates unique constraint "pg_type_typname_nsp_index"`. Both calls run the migration's `CREATE TABLE` at once.
- Lock in place: the test passes 3 runs out of 3.

## Why This Works

goose v3.29.0 creates its version table in `ensureVersionTable` (`provider_run.go:338` in the module cache), which retries every second, up to 3 times. Its comment explains why: some operations, such as `HasPending`, do not take the session locker, so instances can race to create the table on goose's first run.

`Provider.Up` starts with `HasPending`, which takes no lock, and returns early when nothing is pending (`provider.go:244-251`). The lock is taken only after that. So on a fresh database, the two calls race to create the version table before any lock is involved, with or without the locker. One of them loses and retries a second later. By then the other call has applied migration 1, so the late call finds nothing pending and returns. This run concluded that the retry delay serializes the two calls. The retry and the early return are in goose's source; the timing was observed in the 5 passing runs, not traced step by step.

Once the version table exists, which is true for every migration after the first, both calls find the migration pending at the same time. Only the session lock then keeps them apart.

`HasPending` does the setup because it creates the version table without taking the lock (`HasPending` calls `hasPending`, which calls `initialize(ctx, false)` at `provider.go:546`), so the setup leaves no lock held.

## Prevention

- After you write a test for a lock, remove the lock and check that the test fails. A concurrency test that passes without the thing it guards is a false pass.
- When a later migration changes `Migrate` or the provider's options, keep the `HasPending` step in `TestMigrateConcurrently`. Without it, the test passes whether or not the lock works.
- The test takes about 5 seconds. goose's Postgres session locker retries a held lock every 5 seconds (`intervalDuration: 5 * time.Second` in `NewPostgresSessionLocker`, in the `lock` package of goose's module), and the losing call waits one interval. The default was kept, since a shorter interval only speeds up the test.

## Related Issues

- #30, which adds the store's first migration, `Open`, `Migrate` and `InTx`.
- #20, the parent split that #30 is part 3 of.
