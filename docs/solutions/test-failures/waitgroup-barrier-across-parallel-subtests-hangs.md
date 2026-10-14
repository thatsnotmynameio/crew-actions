---
title: A WaitGroup barrier across parallel subtests hangs when one subtest stops early or never starts
date: 2026-10-14
category: test-failures
module: internal/store/postgres
problem_type: test_failure
component: testing_framework
symptoms:
  - "go test -parallel 1 -run TestStoresOfParallelTestsAreIndependent hangs until the -timeout panic, with the first subtest blocked at opened.Wait()"
  - "-run TestStoresOfParallelTestsAreIndependent/first hangs the same way, as only one subtest runs"
  - Before the fix, a t.Fatal in one subtest before its Done left the other subtest waiting forever
root_cause: concurrency
resolution_type: test_fix
severity: medium
tags: [waitgroup, barrier, t-parallel, subtests, deadlock, go-test-timeout, test-isolation, testdb]
---

# A WaitGroup barrier across parallel subtests hangs when one subtest stops early or never starts

## Problem

`TestStoresOfParallelTestsAreIndependent` (`internal/store/postgres/main_test.go:89`) proves that each store test gets a database of its own. It runs two `t.Parallel()` subtests, and lines them up on two `sync.WaitGroup` barriers, `opened` and `inserted`, so that each one inserts a build and then counts the builds while the other's build is also there. On the old shared, truncated database each saw 2 builds, and that was the test's red run in #104.

A barrier between subtests assumes that both subtests reach it. The test runner, not the test, decides that, and a subtest that stops early or never starts leaves the other one waiting forever.

## Symptoms

- With `-parallel 1`, `go test` runs one parallel subtest at a time. The first one waits at `opened.Wait()` (`main_test.go:108`) for a sibling the runner will not start until it ends. The package hangs until `go test -timeout` panics, 10 minutes by default. Reproduced on this branch with `-timeout 20s`: `panic: test timed out after 20s`, with the goroutine at `main_test.go:108`.
- `-run TestStoresOfParallelTestsAreIndependent/first` runs one subtest, which hangs the same way. GOMAXPROCS 1 gives the same result, as `-parallel` defaults to GOMAXPROCS.
- The first version of the test called `opened.Done()` and `inserted.Done()` inline. A `t.Fatal` or `t.Skip` in one subtest before its `Done`, from `newStore` (`testdb.New` calls `t.Fatalf` on a failed `CREATE DATABASE`) or from `createBuild`, ends that goroutine with `runtime.Goexit`, and the other subtest waits forever. The timeout panic then buries the failure that caused it.

## What Didn't Work

- **Calling `Done` inline, right after each step.** It is correct only on the path where every step succeeds. Code review found the deadlock on the failure paths (#104's review, P2, both reviewers).

## Solution

Applied: every exit path releases both barriers. Each subtest wraps each `Done` in `sync.OnceFunc` and defers it, so a `t.Fatal`, `t.Skip` or panic still releases the sibling, and the normal call on the success path still counts once (`main_test.go:102-105`):

```go
openedDone := sync.OnceFunc(opened.Done)
insertedDone := sync.OnceFunc(inserted.Done)
defer openedDone()
defer insertedDone()
s := newStore(t)
openedDone()
opened.Wait()
```

Accepted, not fixed: the hang under `-parallel 1`, GOMAXPROCS 1, or a `-run` that selects one subtest. The review's other option fixes it: drop the barriers, open two stores with `newStore` in one test, insert a build into each, and check that each counts 1. It was not applied because #104's plan asks for two store tests that "run in parallel". Choosing it is a decision on the plan, not a review fix.

## Why This Works

A `WaitGroup` counts calls, not goroutines. A goroutine that ends through `runtime.Goexit` runs its deferred calls, so a deferred `Done` is the only one sure to run. `sync.OnceFunc` keeps the deferred call and the inline one from counting twice, which would make the counter negative and panic.

No change inside a subtest can fix the case where its sibling never starts: the barrier counts 2, and the runner holds the second subtest back until the first returns.

## Prevention

- Before you add a barrier between `t.Parallel()` subtests, check that the property under test needs two goroutines running at once. Here it does not: each `newStore` already gets its own database, so two stores in one test show the same thing with no barrier.
- When a barrier is needed, release it on every exit path with a deferred `sync.OnceFunc(wg.Done)`, and run the test with `-parallel 1` and with `-run` on one subtest, under a short `-timeout`, before merging. Both hang on this test as it is.
- A start barrier between goroutines a test starts itself, as in [concurrent-append-test-passes-without-the-per-file-lock.md](concurrent-append-test-passes-without-the-per-file-lock.md), does not have this problem: the test starts every goroutine, so all of them reach it.

## Related Issues

- #104, which moves the template code to `internal/store/postgres/internal/testdb`, gives each store test its own database, and adds this test.
- [test-template-database-reused-only-once-marked-finished.md](../best-practices/test-template-database-reused-only-once-marked-finished.md): the template that each store test's database is copied from.
- [concurrent-append-test-passes-without-the-per-file-lock.md](concurrent-append-test-passes-without-the-per-file-lock.md): a barrier inside one test, which the test fully controls.
