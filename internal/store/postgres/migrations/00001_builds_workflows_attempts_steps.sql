-- +goose Up

-- Builds, their workflows, each workflow's attempts and each attempt's
-- steps.
--
-- Every row carries its repository's identity: the host, the organization
-- and the repository, parsed from a URL that is not kept. Each child's
-- foreign key includes them, so a row never disagrees with its build.
--
-- States and outcomes are text with CHECK constraints, not enums, so a new
-- state is one migration that replaces a constraint. A row has an outcome
-- and a finished_at exactly when it is finished (or, for a step, when it
-- ended skipped, without an outcome).
--
-- Outcomes stay success and failure only (docs/architecture.md, D4). A reason, such as a runner
-- that was lost, gets a column of its own, never another outcome.
--
-- Foreign keys restrict deletes.

CREATE TABLE builds (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host text NOT NULL CHECK (host <> ''),
    organization text NOT NULL CHECK (organization <> ''),
    repository text NOT NULL CHECK (repository <> ''),
    state text NOT NULL CHECK (state IN ('running', 'finished')),
    outcome text CHECK (outcome IN ('success', 'failure')),
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (id, host, organization, repository),
    CONSTRAINT builds_outcome_when_finished CHECK ((state = 'finished') = (outcome IS NOT NULL)),
    CONSTRAINT builds_finished_at_when_finished CHECK ((state = 'finished') = (finished_at IS NOT NULL))
);

-- version goes up on every state change, and accepting a workflow compares
-- it. assigned_at is when the workflow was handed out.
CREATE TABLE workflows (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id bigint NOT NULL,
    host text NOT NULL CHECK (host <> ''),
    organization text NOT NULL CHECK (organization <> ''),
    repository text NOT NULL CHECK (repository <> ''),
    name text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'assigned', 'running', 'finished')),
    outcome text CHECK (outcome IN ('success', 'failure')),
    version bigint NOT NULL DEFAULT 0,
    -- The compiled workflow, which the store neither parses nor checks.
    definition bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    assigned_at timestamptz,
    finished_at timestamptz,
    UNIQUE (build_id, name),
    UNIQUE (id, host, organization, repository),
    CONSTRAINT workflows_build_fkey FOREIGN KEY (build_id, host, organization, repository)
        REFERENCES builds (id, host, organization, repository) ON DELETE RESTRICT,
    CONSTRAINT workflows_outcome_when_finished CHECK ((state = 'finished') = (outcome IS NOT NULL)),
    CONSTRAINT workflows_finished_at_when_finished CHECK ((state = 'finished') = (finished_at IS NOT NULL))
);

-- The hand-out takes the oldest pending workflow first.
CREATE INDEX workflows_pending ON workflows (created_at) WHERE state = 'pending';

-- An attempt is open until it has a finished_at. accepted_version is the
-- workflow's version when the runner accepted it.
CREATE TABLE attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workflow_id bigint NOT NULL,
    host text NOT NULL CHECK (host <> ''),
    organization text NOT NULL CHECK (organization <> ''),
    repository text NOT NULL CHECK (repository <> ''),
    number integer NOT NULL CHECK (number >= 1),
    accepted_version bigint NOT NULL,
    outcome text CHECK (outcome IN ('success', 'failure')),
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (workflow_id, number),
    UNIQUE (id, host, organization, repository),
    CONSTRAINT attempts_workflow_fkey FOREIGN KEY (workflow_id, host, organization, repository)
        REFERENCES workflows (id, host, organization, repository) ON DELETE RESTRICT,
    CONSTRAINT attempts_outcome_when_finished CHECK ((finished_at IS NOT NULL) = (outcome IS NOT NULL))
);

-- A workflow has at most one open attempt.
CREATE UNIQUE INDEX attempts_one_open_per_workflow ON attempts (workflow_id) WHERE finished_at IS NULL;

-- A new attempt starts from the first step, so each attempt has steps of
-- its own.
CREATE TABLE steps (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    attempt_id bigint NOT NULL,
    host text NOT NULL CHECK (host <> ''),
    organization text NOT NULL CHECK (organization <> ''),
    repository text NOT NULL CHECK (repository <> ''),
    position integer NOT NULL,
    name text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'running', 'finished', 'skipped')),
    outcome text CHECK (outcome IN ('success', 'failure')),
    exit_status integer,
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (attempt_id, position),
    CONSTRAINT steps_attempt_fkey FOREIGN KEY (attempt_id, host, organization, repository)
        REFERENCES attempts (id, host, organization, repository) ON DELETE RESTRICT,
    CONSTRAINT steps_outcome_when_finished CHECK ((state = 'finished') = (outcome IS NOT NULL)),
    CONSTRAINT steps_finished_at_when_ended CHECK ((state IN ('finished', 'skipped')) = (finished_at IS NOT NULL))
);

-- +goose Down

DROP TABLE steps;
DROP TABLE attempts;
DROP TABLE workflows;
DROP TABLE builds;
