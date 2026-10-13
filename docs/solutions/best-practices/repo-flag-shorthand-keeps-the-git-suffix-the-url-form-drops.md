---
title: The --repo shorthand keeps a .git suffix that the URL form drops, so one repository can get two identities
date: 2026-10-13
category: best-practices
module: internal/model
problem_type: best_practice
component: data_model
severity: medium
applies_when:
  - Code reads a repository's identity from --repo instead of from the origin remote
  - A change touches ParseRepositoryFlag or ParseRepository in internal/model
  - Builds are looked up or grouped by host, organization and repository
  - A plan or a doc states that parsing always drops the .git suffix
tags: [repository-identity, repo-flag, git-suffix, parse-repository, host-organization-repository, accepted-risk]
---

# The --repo shorthand keeps a .git suffix that the URL form drops, so one repository can get two identities

## Context

A repository's identity is its host, organization and name. It is parsed once and the URL is not kept. Issue #29 built `ParseRepository` for URLs and git's scp form. Its plan's R10 says "Parsing drops any user, credential, scheme and `.git` suffix" (`docs/plans/2026-10-13-0238-issue-29-plan.md`), and the plan for #31 says the same. The `Repository.Name` doc comment says "Name has no .git suffix" (`internal/model/repository.go:18`), and `ParseRepository` strips one `.git` (`internal/model/repository.go:38`).

Issue #69 added `ParseRepositoryFlag` for `--repo` (`internal/model/repository.go:65`). A value with `://`, or with a `:` before its first `/`, goes to `ParseRepository`. Anything else is the `host/organization/repository` shorthand. It is split on `/`, and the last part becomes the name as it is (`internal/model/repository.go:91`), with no `.git` trimmed.

That was a choice. The implementation left the suffix on because the plan for #69 did not ask for trimming it, and because a repository name can end in `.git`. The review of #69 flagged the result as a P3 residual risk and did not fix it. The code, the tests and the plans do not record it: R10 and the doc comment still say the suffix is always dropped.

## Guidance

- Do not assume `Repository.Name` never ends in `.git`. That holds for a URL or an scp value, but not for the `--repo` shorthand.
- The two forms disagree on one input:

  | Input | Parser | Name |
  |---|---|---|
  | `https://github.com/org/repo.git` (an `origin`) | `ParseRepository` | `repo` |
  | `git@github.com:org/repo.git` | `ParseRepository` | `repo` |
  | `--repo github.com/org/repo.git` | `ParseRepositoryFlag`, shorthand | `repo.git` |
  | `--repo github.com/org/repo` | `ParseRepositoryFlag`, shorthand | `repo` |

- To make the forms agree, change `ParseRepositoryFlag`: trim one `.git` from the last part, refuse the value when that leaves the part empty, and add `github.com/org/repo.git` to `TestParseRepositoryFlag` in `internal/model/repository_test.go`. Rows written before the change may still hold `repo.git`.
- If the suffix is kept on purpose, say so in the `Repository.Name` doc comment, so it stops promising "no .git suffix".
- Do not send the shorthand through `ParseRepository` to get the trimming. `ParseRepository` refuses a value with no scheme and no scp `:` because an `origin` that is a local path is not a remote (`internal/model/repository.go:119-131`). That refusal is needed for `origin`.

## Why This Matters

Builds are stored and found by host, organization and repository. Once `run` reads `origin` (planned in #22), a build from a checkout whose `origin` is `https://github.com/org/repo.git` and a build run with `--repo github.com/org/repo.git` will put the same repository under two names, `repo` and `repo.git`. Nothing fails. The builds are just split, and the split shows up only when someone looks for them.

The keep-the-suffix argument is also weaker than it looks. The URL form can only name a repository that really ends in `.git` by doubling the suffix (`Repo.git.git` gives `Repo.git`). So the URL form already treats a final `.git` as decoration, and the shorthand's choice is the exception.

## When to Apply

- When wiring `run`'s `--repo` and its reading of `origin` (later parts of #22, such as #78), or any code that compares identities from the two sources.
- Before writing a query or a test that assumes a name never ends in `.git`.
- When citing R10 from the plans for #29 or #31: it describes `ParseRepository` only.

## Examples

What the code does now:

```go
model.ParseRepository("https://github.com/org/repo.git")
// Repository{Host: "github.com", Organization: "org", Name: "repo"}

model.ParseRepositoryFlag("github.com/org/repo.git")
// Repository{Host: "github.com", Organization: "org", Name: "repo.git"}
```

The fix the review proposed, at the end of `ParseRepositoryFlag`:

```go
name := strings.TrimSuffix(segments[last], ".git")
if name == "" {
	return Repository{}, errors.New("--repo has an empty repository name")
}
```

## Related

- Issue #69 (this change), #29 (`ParseRepository` and R10), #31 (R10 restated), #78 and #22 (where `--repo` and `origin` meet).
- `docs/plans/2026-10-13-1434-issue-69-plan.md`, U2.
