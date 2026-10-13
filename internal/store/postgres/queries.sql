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

-- name: WorkflowExists :one
SELECT EXISTS (SELECT 1 FROM workflows WHERE id = $1);
