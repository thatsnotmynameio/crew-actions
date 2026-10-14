# AGENTS.md

Guidance for coding agents working in this repository.

crew actions is an actions engine built alongside [crew](https://github.com/thatsnotmynameio/crew): YAML workflows, runners that pull work, and a queue in Postgres.

Read [`docs/architecture.md`](docs/architecture.md) before any change. It fixes who owns what, the names, the folders and the import rules. Where it and [`docs/exploration/`](docs/exploration/) disagree, the architecture wins.

The import rules of its section 4 are checked by `depguard` in golangci-lint. Run it from the repository root, at the version CI runs:

```sh
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run
```

The store's tests run against the PostgreSQL 18 of [`compose.yaml`](compose.yaml) and read its URL from the environment. From the repository root:

```sh
docker compose up --detach --wait test-postgres
export CREW_ACTIONS_TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5433/crew_actions_test?sslmode=disable'
go test ./...
```

- Without the variable the store's tests skip, and `go test` shows the skip only with `-v`.
- Only one test run at a time should use the database for now: each test gets a database of its own, but the store's tests name their empty databases `empty_1`, `empty_2` and so on, so two runs at once collide.
- After changing a migration or switching branches, run `docker compose down` and start the server again, so the tests begin from an empty server.
- `docker compose down` stops the server for every worktree, since they all share it.
