---
title: A concurrent append test passes without the per-file lock it guards
date: 2026-10-13
category: test-failures
module: internal/store/logs
problem_type: test_failure
component: testing_framework
symptoms:
  - TestTheSameChunkAppendedAtOnceIsHeldOnce passed 5 single runs out of 5 with the per-file lock removed from Dir.Append
  - Only go test -count=200 made it fail, in 88 of the 200 iterations, with the chunk written twice
root_cause: concurrency
resolution_type: test_fix
severity: medium
tags: [lock, mutex, false-pass, mutation-testing, race-detector, goroutines, start-barrier, logs, append-by-offset, concurrency]
---

# A concurrent append test passes without the per-file lock it guards

## Problem

`Dir.Append` takes one lock per log file (`internal/store/logs/logs.go:75-77`), so that a chunk resent at the same time as its first copy is written once. Without the lock, two appends can both read the file's size with `Stat` (`logs.go:88`) before either one writes (`logs.go:101`), and then both write the chunk. The first version of `TestTheSameChunkAppendedAtOnceIsHeldOnce`, the only test of that lock, started two goroutines that appended the same chunk. It passed with the lock removed.

## Symptoms

- With `lock.Lock()` and `lock.Unlock()` removed (applied through `go test -overlay`, so the checkout stayed untouched), `go test -race -count=1 -run TestTheSameChunkAppendedAtOnceIsHeldOnce` printed `ok` in 5 runs out of 5.
- Only `-count=200` failed it: 88 of the 200 iterations failed, with `file holds "01234567890123456789"`: the 10-byte chunk written twice.

## What Didn't Work

- **Two goroutines started one after the other with `wg.Go`.** The first one usually finishes its `Stat` and `Write` before the second one starts, so they rarely meet in the gap between the two calls.
- **Running the test under `-race`.** The race detector sees only Go memory shared without synchronization. What the two appends share here is the file on disk, which the detector never sees, so it has nothing to report with or without the lock.
- **Seeing the test fail first.** Step 1 of #71 wrote the tests first and saw this one fail, but against a stub `Append` that wrote every chunk without the offset rules (per that step's notes). That stub writes the chunk twice even when the two appends run one after the other, so the failure came from the missing offset rules, not from the missing lock. It said nothing about whether the test catches a missing lock.

## Solution

Start many appends on one barrier, and repeat the race over many fresh files in one run. `TestTheSameChunkAppendedAtOnceIsHeldOnce` (`internal/store/logs/logs_test.go:153`) now does this for 50 attempt ids, with 8 goroutines each:

```go
start := make(chan struct{})
var wg sync.WaitGroup
held := make([]int64, 8)
errs := make([]error, 8)
for i := range held {
	wg.Go(func() {
		<-start
		held[i], errs[i] = d.Append(a, position, 0, []byte(chunk))
	})
}
close(start)
wg.Wait()
// every answer is 10, and reading the log back gives the chunk once
```

Mutation check after the fix, run on the committed test by the step of #71 that applied it (session history): with the lock removed through the same overlay, the test failed in 5 single runs out of 5. With the lock in place it passes, also under `go test -race -count=3`.

## Why This Works

All 8 goroutines block on `<-start`, so when it closes they all leave at about the same moment, and several of them land between one append's `Stat` and its `Write`. Fifty fresh files give the race fifty chances per run, so one run fails without the lock instead of one run in a few dozen.

## Prevention

- After you write a test for a lock, remove only the lock and check that the test fails in a single run. A failure against a stub that also lacks other rules does not count. This is the second time the repo has had this false pass. The first was a goose session-lock test with a different cause, recorded in [goose-concurrent-migrate-test-passes-without-the-lock.md](goose-concurrent-migrate-test-passes-without-the-lock.md).
- `go test -overlay` lets you apply the mutation without editing the checkout: point it at a copy of the file with the lock removed.
- Do not count on `-race` to test a lock that guards a file or a database. Only a test whose result changes when the lock is gone can show that the lock works.
- For an in-process lock, start the competing calls on a shared channel and repeat over fresh keys within one test, rather than relying on `-count` to find the race.
- Keep the barrier and the 50 by 8 loop if the test changes. Two goroutines alone make it pass again whether or not the lock works.

## Related Issues

- #71, which adds `internal/store/logs` and this test.
- [goose-concurrent-migrate-test-passes-without-the-lock.md](goose-concurrent-migrate-test-passes-without-the-lock.md), the first instance of a lock test that passes without its lock (#30).
