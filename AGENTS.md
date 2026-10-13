# AGENTS.md

Guidance for coding agents working in this repository.

crew actions is an actions engine built alongside [crew](https://github.com/thatsnotmynameio/crew): YAML workflows, runners that pull work, and a queue in Postgres.

Read [`docs/architecture.md`](docs/architecture.md) before any change. It fixes who owns what, the names, the folders and the import rules. Where it and [`docs/exploration/`](docs/exploration/) disagree, the architecture wins.

The import rules of its section 4 are checked by `depguard` in golangci-lint. Run it from the repository root, at the version CI runs:

```sh
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run
```
