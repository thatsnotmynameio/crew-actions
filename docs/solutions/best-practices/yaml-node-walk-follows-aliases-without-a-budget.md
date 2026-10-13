---
title: Walking a yaml.Node skips yaml.v3's alias guard, so the walk must handle aliases itself
date: 2026-10-13
category: best-practices
module: internal/spec
problem_type: best_practice
component: data_model
severity: high
applies_when:
  - Code decodes YAML into a go.yaml.in/yaml/v3 yaml.Node and walks the tree by hand
  - The YAML comes from someone other than the operator, such as a workflow file pushed to a repository
  - A walk checks mapping keys against an allowed list
  - A walk reports refusals with a node's line
tags: [yaml, yaml-node, alias, anchor, billion-laughs, cwe-776, workflow-file, validation]
---

# Walking a yaml.Node skips yaml.v3's alias guard, so the walk must handle aliases itself

## Context

Issue #34 parses workflow files in `internal/spec` by decoding into a `yaml.Node` and walking it (plan KTD3, `docs/plans/2026-10-13-0305-issue-34-plan.md`). This gives errors that carry a key, a path and a line. While building it, a helper `resolve` was added to follow YAML aliases wherever the walk reads a node (`internal/spec/spec.go:163`). Neither the plan nor the code says anything about anchors or aliases. The review of #34 (ce-code-review run `20261013-031236-1b643012`) found three problems with this, and each was reproduced against the branch. The plan, the code and the tests do not record any of them.

1. **No alias budget.** yaml.v3 refuses input that expands too much through aliases ("document contains excessive aliasing"). It runs that check only when it decodes into Go values. In `decoder.unmarshal`, the ratio check comes first, but for a `yaml.Node` target the function stores the node and returns at once (`decode.go:484-495` of go.yaml.in/yaml/v3 v3.0.5, in the Go module cache). The node keeps its aliases as `AliasNode`s that point at their anchors, so the tree stays the size of the input. The expansion happens later, in our own walk, and nothing limits it there. An 82,926-byte file that anchors a 2,000-item `commands` list and points 2,000 steps at it with `commands: *c` made `Parse` return 4,002,000 commands and allocate 267 MB, with no error. The review measured 100 million commands and about 6.9 GB from a 409 KB file. The cost grows with the square of the file size.
2. **Alias keys pass the key allow list.** For an alias, the parser sets the node's `Value` to the anchor's label (`decode.go:214-216`). `fields` checks `key.Value` against the allowed keys (`spec.go:183`), so in `steps: [{commands: &name [echo ok], *name: build}]` the key `*name` passes as the allowed key `name`. `Parse` returns a step named `build` with no error, although the step never has a real `name` key.
3. **Refusals point at the anchor.** `step` hands `resolve(item)` to `checkCommand` (`spec.go:272`), and `checkCommand` reports `n.Line` (`spec.go:299-303`), which is the anchor's line. A command written as `- *commands` on line 6, where the alias stands for a list, is refused with `steps[1].commands[0] (line 3)`, and line 3 is the valid step that defines the anchor.

## Guidance

When code walks a `yaml.Node`, it owns alias handling. yaml.v3 does none of it for the walk.

- **Choose an alias policy explicitly, and write it into the format.** Either refuse every `AliasNode`, which is the simplest and fixes all three problems with one check, or allow aliases only where they cannot amplify.
- **Never follow an alias to a sequence or a mapping without a budget.** Only those multiply the work. A scalar alias costs one node each time it is used. Refuse a sequence or mapping alias at the alias's own line, or keep a running total of the items the walk produces and stop when it passes a cap.
- **Check a key's `Kind` before its `Value`.** A key must be a `yaml.ScalarNode` before it is compared with the allowed keys. An `AliasNode` key carries the anchor's label in `Value`, and a mapping or sequence key carries nothing useful there.
- **Report the line where the alias is used.** Keep the unresolved node and take the line from it, and use the resolved node only for its content.

## Why This Matters

The engine is meant to read workflow files from each repository's `.crew/workflows/` directory, through GitHub events (`docs/architecture.md`). Anyone who can push such a file, for example in a pull request, controls the input to `Parse`. A file a few megabytes long would exhaust the memory of whatever process parses it. yaml.v3's guard leaves the impression that alias bombs are already handled, and they are, but only on the decode path this design does not use. The other two problems weaken guarantees the format makes: that any key outside the allowed list is refused (`spec.go:21`), and that each refusal names the line at fault (KTD5).

Later parts of #34 inherit this walk. Part 4 sorts refused keys into "not supported yet" or "unknown", and part 5 adds `retry`. Each new key or level is another place where an alias can be followed.

## When to Apply

- Any new `yaml.Node` walk in this repository, and any new key or nesting level added to `internal/spec`.
- Code that switches to a `yaml.Node` decode from a struct decode, which loses the guard without any visible change.

## Examples

Status as of this writing: all three findings are open on branch `crew/issue-34-development`. The review rated #1 P1 and the other two P3, and nothing was applied, because the fix waits on a decision about whether workflow files may use anchors at all (review finding #2). The review suggested this fix if aliases to scalars stay allowed:

```go
// in fields, before the allowed-key check
if key.Kind != yaml.ScalarNode {
	return nil, &Error{Path: keyPath, Line: key.Line, Msg: "a key must be plain text"}
}
// before resolving a value, or an item of steps or commands
if v := n.Content[i+1]; v.Kind == yaml.AliasNode && v.Alias.Kind != yaml.ScalarNode {
	return nil, &Error{Path: keyPath, Line: v.Line, Msg: "an alias may only stand for a single value; write the list out"}
}
```

Tests that pin each case:

- `steps:` with an anchored `commands: &c` list and a second step with `commands: *c` is refused at the second step's line, or by the cap if a budget is chosen instead.
- `steps: [{commands: &name [echo ok], *name: build}]` is refused at the alias key's line.
- `- *commands` as a command item, where `*commands` stands for a list, is refused at the line of the `- *commands` item, not at the anchor's line.

## Related

- Issue #34, Parse the workflow file format in internal/spec
- `docs/plans/2026-10-13-0305-issue-34-plan.md`, KTD3 and KTD5
