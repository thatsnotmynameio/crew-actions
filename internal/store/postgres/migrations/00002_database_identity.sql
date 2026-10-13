-- +goose Up

-- The database's identity: one row with a random id, made when the
-- database is migrated. The engine names its log directory after it, so a
-- database made again never shows the logs of the one it replaces.

CREATE TABLE database_identity (
    -- one is always true, so the table holds at most one row.
    one boolean PRIMARY KEY DEFAULT true CHECK (one),
    id uuid NOT NULL DEFAULT gen_random_uuid()
);

INSERT INTO database_identity DEFAULT VALUES;

-- +goose Down

DROP TABLE database_identity;
