# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Repositories

### Repository identity
The name every build carries for the repository it came from: its host, its organization and its repository name, read once from a remote URL or from a value the submitter gives, with the URL itself never kept.
*Avoid:* repository URL, remote, origin (each is only a source the identity is read from)

The host is lowercased and has no port; the organization holds every level of nested groups; the organization and the name keep the case they were given. A user or a credential in the source is dropped and never stored or printed. The HTTPS and SSH URLs of one repository give one identity. Two sources that spell the same repository differently, such as a name with and without a trailing `.git` given by hand, can give two identities.
