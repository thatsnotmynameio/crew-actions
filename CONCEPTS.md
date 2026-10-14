# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Repositories

### Repository identity
The name every build carries for the repository it came from: its host, its organization and its repository name, read once from a remote URL or from a value the submitter gives, with the URL itself never kept.
*Avoid:* repository URL, remote, origin (each is only a source the identity is read from)

The host is lowercased and has no port; the organization holds every level of nested groups; the organization and the name keep the case they were given. A user or a credential in the source is dropped and never stored or printed. The HTTPS and SSH URLs of one repository give one identity. Two sources that spell the same repository differently, such as a name with and without a trailing `.git` given by hand, can give two identities.

## Builds

### Build
One run of a repository's pipeline, started by one event, holding the workflows that run for it.

### Workflow
One file of the pipeline within a build, and the unit the queue hands to a runner: it goes whole to one runner, and its steps share one workspace. Its name is unique within its build.

A workflow can be sent back to the queue, for example when a runner's claim on it expires, and is then run again as a new attempt.

### Attempt
One run of a workflow, numbered from 1 within that workflow, holding the steps of that run.

A workflow has at most one open attempt at a time. A new attempt starts again from the first step, with steps of its own, so the same step position recurs in every attempt; a step is named by its workflow, its attempt and its position, never by its position alone.

### Step
A command, or a plugin, run in order within one attempt of a workflow, at a position unique within that attempt. Each step has its own state, outcome and output.
