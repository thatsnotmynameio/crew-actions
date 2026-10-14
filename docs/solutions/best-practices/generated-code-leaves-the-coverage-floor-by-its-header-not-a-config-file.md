---
title: Generated code leaves the coverage floor by its "Code generated" line, not by a config file, and only where the pinned workflow has the rule
date: 2026-10-14
category: best-practices
module: .github (the organization's Go check)
problem_type: best_practice
component: infrastructure
severity: high
applies_when:
  - A change adds generated Go code (buf, protoc plugins, sqlc or another generator) and CI checks the 90% coverage floor
  - A plan or a doc says .testcoverage.yml excludes generated paths from the floor
  - The coverage total in CI is far below the total of the hand-written code
  - A change moves or keeps the SHA at which .github/workflows/ci.yml or the organization's ruleset pins thatsnotmynameio/github-actions' go.yml
  - A generator is added whose output may not carry Go's standard generated-file line
tags: [coverage-floor, generated-code, code-generated-header, testcoverage, github-actions, workflow-pin, buf, sqlc]
---

# Generated code leaves the coverage floor by its "Code generated" line, not by a config file, and only where the pinned workflow has the rule

## Context

Issue #72 added the protobuf contracts and committed their generated Go code: `internal/protocol/protocol.pb.go`, `internal/api/api.pb.go` and the two `*.connect.go` files under `internal/transport/connect/`. Its plan, `docs/plans/2026-10-14-0215-issue-72-plan.md`, says in KTD15: "`.testcoverage.yml` excludes the generated paths from the coverage floor". Its risk list, U5's file list and the Verification Contract's coverage gate say the same.

That file no longer existed when the work started. #89 moved the Go checks to the organization's workflow, the `go.yml` workflow of thatsnotmynameio/github-actions. This repository now gives that workflow only its settings in `.github/go.yml`, with `coverage: 90` as the floor (`.github/go.yml:10`). At that time the workflow's floor counted every statement in the profile. With the generated code the total was 57.7%, against 92.2% for the hand-written code, so the first development run of #72 stopped as blocked (session history).

thatsnotmynameio/github-actions#14, "fix: leave generated files out of the Go coverage floor", merged on 2026-10-14. Since then, the floor step leaves out every `.go` file that has a line matching `^// Code generated .* DO NOT EDIT\.$` before its `package` clause. golangci-lint and gopls use the same rule. Run locally on #72's branch, the step printed 93.2%.

The plans still name `.testcoverage.yml`, and plans are not updated after they ship. These plans name it: those of issues 28, 29, 30, 31, 32, 33, 34, 35, 36, 52, 53, 55, 68, 69 and 72 in `docs/plans/`.

## Guidance

1. **Do not add an exclusion list.** No repository file excludes generated code, and none is needed. The line in the generated file is the exclusion. protoc-gen-go, protoc-gen-connect-go and sqlc all write it. buf copies a proto file's leading comment above the line, which is fine because the line still comes before `package` (`internal/api/api.pb.go:1-4`).
2. **Check a new generator's output for the line.** A generator that does not write it, or writes it after `package`, counts against the floor. A file written by hand that carries the line drops out of the floor. The step lists the files it leaves out under "Generated files, left out of the total:", so a reviewer can check that list.
3. **Check which version of the workflow runs.** The rule exists only in versions of the organization's `go.yml` from #14 on. Each caller pins a SHA:
   - `.github/workflows/ci.yml:35` runs the check on each push to main. As of this writing it pins `go.yml@e72ce0d0a5da8284edb9aea1cdff812e06c80b54`. That version predates #14 and its floor step has no generated-file rule (checked with `gh api repos/thatsnotmynameio/github-actions/contents/.github/workflows/go.yml?ref=<sha>`). Unless the pin moves to #14's merge or later, the first push to main that includes generated protobuf code fails its floor at about 58%.
   - The organization's ruleset runs the pull request check, "tnmn.io: go", at its own pinned SHA. #14 says the rulesets pinned `aa829fc` and need to move to its merge for the change to reach pull requests. This session could not read the rulesets (the API returned 403), so whether they moved is unverified.
4. **Read old plans' coverage gate as the organization's floor.** Where a plan says ".testcoverage.yml" or "go-test-coverage", the check that runs today is the floor step of the organization's `go.yml`, with the floor from `.github/go.yml`.

## Why This Matters

Generated code is large and has no tests of its own. Here it is about 2,200 lines against 230 lines of hand-written conversions, and counting it drags the total far below the floor however well the hand-written code is tested. A plan that relies on a config file sends the next agent looking for a file that does not exist, or makes it add one that nothing reads. A fix in the organization's workflow helps only the callers whose pin includes it, so a check can pass on the pull request and fail on main, or the other way round.

## When to Apply

- Before adding or regenerating generated Go code.
- When CI's coverage total and a local total of the hand-written code disagree by a wide margin.
- When a dependency update moves the SHA pin of the organization's `go.yml`, in `ci.yml` or in the ruleset.
- When planning a part that names `.testcoverage.yml` or go-test-coverage.

## Examples

Measure the floor as CI does, from the repository root, with the test database up and `CREW_ACTIONS_TEST_DATABASE_URL` set as `AGENTS.md` says:

```sh
go test -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
# The floor step of the organization's go.yml, from #14 on: list the files
# whose "Code generated ... DO NOT EDIT." line comes before package...
find . -name '*.go' -not -path './vendor/*' -exec awk '
  /^\/\/ Code generated .* DO NOT EDIT\.$/ { print substr(FILENAME, 3); nextfile }
  /^package[ \t]/ { nextfile }
' {} + > generated.txt
# ...drop their profile lines and take the total.
awk -v module="$(go list -m)" '
  FILENAME == ARGV[1] { generated[module "/" $0]; next }
  FNR == 1 || !(substr($0, 1, index($0, ":") - 1) in generated)
' generated.txt coverage.out > coverage.own.out
go tool cover -func=coverage.own.out | tail -1
```

On #72's branch, `generated.txt` lists seven files: the two `*.pb.go`, the two `*.connect.go`, and sqlc's `db.go`, `models.go` and `queries.sql.go` under `internal/store/postgres/queries/`. The total is 93.2%, while `go tool cover -func=coverage.out` on the raw profile gives 57.7%.
