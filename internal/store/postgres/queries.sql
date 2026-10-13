-- name: InsertBuild :one
INSERT INTO builds (host, organization, repository, state)
VALUES ($1, $2, $3, 'running')
RETURNING id;

-- A workflow starts at version 1, and its version goes up on every state
-- change.

-- name: InsertWorkflow :exec
INSERT INTO workflows (build_id, host, organization, repository, name, state, version, definition)
VALUES ($1, $2, $3, $4, $5, 'pending', 1, $6);

-- name: GetBuild :one
SELECT id, host, organization, repository, state, outcome, created_at, finished_at
FROM builds
WHERE id = $1;

-- name: ListWorkflows :many
SELECT id, name, state, outcome, version, definition, created_at, assigned_at, finished_at
FROM workflows
WHERE build_id = $1
ORDER BY id;

-- name: ListAttempts :many
SELECT a.id, a.workflow_id, a.number, a.accepted_version, a.outcome, a.started_at, a.finished_at
FROM attempts a
JOIN workflows w ON w.id = a.workflow_id
WHERE w.build_id = $1
ORDER BY a.workflow_id, a.number;

-- name: ListSteps :many
SELECT s.id, s.attempt_id, s.position, s.name, s.state, s.outcome, s.exit_status, s.started_at, s.finished_at
FROM steps s
JOIN attempts a ON a.id = s.attempt_id
JOIN workflows w ON w.id = a.workflow_id
WHERE w.build_id = $1
ORDER BY s.attempt_id, s.position;

-- PendingWorkflows locks the pending workflows it returns, and skips those
-- another transaction holds, until its transaction ends.

-- name: PendingWorkflows :many
SELECT id, version, name, build_id, created_at
FROM workflows
WHERE state = 'pending'
ORDER BY created_at, id
LIMIT sqlc.arg(n)::bigint
FOR UPDATE SKIP LOCKED;

-- name: AssignWorkflow :one
UPDATE workflows
SET state = 'assigned', version = version + 1, assigned_at = now()
WHERE id = $1 AND state = 'pending' AND version = $2
RETURNING version, definition;

-- name: WorkflowDefinition :one
SELECT definition FROM workflows WHERE id = $1;

-- name: WorkflowExists :one
SELECT EXISTS (SELECT 1 FROM workflows WHERE id = $1);

-- Accepting a workflow moves it from assigned to running at the version
-- its hand-out returned.

-- name: StartWorkflow :execrows
UPDATE workflows
SET state = 'running', version = version + 1
WHERE id = $1 AND state = 'assigned' AND version = $2;

-- An attempt and its steps copy their parent's identity. A workflow's
-- attempts are numbered from 1, and accepting locks the workflow's row,
-- so two attempts never take one number.

-- name: InsertAttempt :one
INSERT INTO attempts (workflow_id, host, organization, repository, number, accepted_version)
SELECT w.id, w.host, w.organization, w.repository,
       (SELECT coalesce(max(a.number), 0) + 1 FROM attempts a WHERE a.workflow_id = w.id),
       sqlc.arg(accepted_version)::bigint
FROM workflows w
WHERE w.id = sqlc.arg(workflow_id)::bigint
RETURNING id;

-- name: InsertStep :exec
INSERT INTO steps (attempt_id, host, organization, repository, position, name, state)
SELECT a.id, a.host, a.organization, a.repository, sqlc.arg(position)::integer, sqlc.arg(name)::text, 'pending'
FROM attempts a
WHERE a.id = sqlc.arg(attempt_id)::bigint;

-- name: AttemptWorkflow :one
SELECT a.workflow_id, w.build_id, a.number
FROM attempts a
JOIN workflows w ON w.id = a.workflow_id
WHERE a.id = $1;

-- LockAttempt locks the attempt until its transaction ends, so its steps
-- change only while it is open, and never while it finishes.

-- name: LockAttempt :one
SELECT finished_at FROM attempts WHERE id = $1 FOR UPDATE;

-- name: StartStep :execrows
UPDATE steps
SET state = 'running', started_at = now()
WHERE attempt_id = $1 AND position = $2 AND state = 'pending';

-- name: FinishStep :execrows
UPDATE steps
SET state = 'finished', outcome = sqlc.arg(outcome)::text, exit_status = sqlc.narg(exit_status), finished_at = now()
WHERE attempt_id = sqlc.arg(attempt_id) AND position = sqlc.arg(position) AND state = 'running';

-- name: SkipStep :execrows
UPDATE steps
SET state = 'skipped', finished_at = now()
WHERE attempt_id = $1 AND position = $2 AND state = 'pending';

-- name: StepExists :one
SELECT EXISTS (SELECT 1 FROM steps WHERE attempt_id = $1 AND position = $2);

-- An attempt succeeds only once each of its steps has finished with
-- success or been skipped.

-- name: StepsBlockSuccess :one
SELECT EXISTS (
    SELECT 1 FROM steps
    WHERE attempt_id = $1 AND (state NOT IN ('finished', 'skipped') OR outcome = 'failure')
);

-- Failing an attempt closes its leftover steps: a pending step is skipped,
-- and a running one finishes with failure and no exit status.

-- name: SkipPendingSteps :exec
UPDATE steps
SET state = 'skipped', finished_at = now()
WHERE attempt_id = $1 AND state = 'pending';

-- name: FailRunningSteps :exec
UPDATE steps
SET state = 'finished', outcome = 'failure', finished_at = now()
WHERE attempt_id = $1 AND state = 'running';

-- name: FinishAttempt :exec
UPDATE attempts
SET outcome = sqlc.arg(outcome)::text, finished_at = now()
WHERE id = sqlc.arg(id);

-- name: FinishWorkflow :execrows
UPDATE workflows
SET state = 'finished', outcome = sqlc.arg(outcome)::text, version = version + 1, finished_at = now()
WHERE workflows.id = sqlc.arg(id)::bigint AND workflows.state = 'running'
  AND NOT EXISTS (SELECT 1 FROM attempts a WHERE a.workflow_id = workflows.id AND a.finished_at IS NULL);

-- LockBuild locks the build until its transaction ends.

-- name: LockBuild :one
SELECT state FROM builds WHERE id = $1 FOR UPDATE;

-- name: UnfinishedWorkflows :one
SELECT EXISTS (SELECT 1 FROM workflows WHERE build_id = $1 AND state <> 'finished');

-- name: FinishBuild :exec
UPDATE builds
SET state = 'finished', outcome = sqlc.arg(outcome)::text, finished_at = now()
WHERE id = sqlc.arg(id);

-- name: DatabaseID :one
SELECT id::text FROM database_identity;
