-- Commission system of record (HEU-555). Deliberately relational, not
-- event-sourced: design-rationale 027 requires commission data to survive
-- independent of ADR-003's default Compact retention, which purges raw
-- events after 90 days.
--
-- Tables are unqualified. DEVELOPMENT.md assigns Network Engine a
-- `network_engine` schema, but no migration in this project creates a schema
-- and every store queries unqualified names. Introducing physical schemas is
-- a project-wide change and is not part of this ticket.
CREATE TABLE commission_runs (
    id            UUID        PRIMARY KEY,
    period_id     TEXT        NOT NULL,
    plan_hash     TEXT        NOT NULL,
    status        TEXT        NOT NULL,
    carry_forward JSONB,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    voided_at     TIMESTAMPTZ,
    -- Must reference a run for the same period. Not expressible as a foreign
    -- key. Enforced in Go inside ReplaceRun's transaction, not by a trigger.
    superseded_by UUID        REFERENCES commission_runs(id),
    CHECK (period_id <> ''),
    -- The full digest, not just the namespace. LIKE 'sha256:%' would accept
    -- 'sha256:' and 'sha256:anything', which are not hashes.
    CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('running', 'complete', 'voided')),
    -- One-directional on purpose. A completed run must have a completion
    -- time, and a voided run keeps whatever completion time it had. A
    -- biconditional would make complete -> voided impossible without erasing
    -- completed_at, destroying the audit fact the row exists to hold.
    CHECK (status <> 'complete' OR completed_at IS NOT NULL),
    CHECK ((status = 'voided') = (voided_at IS NOT NULL)),
    CHECK (superseded_by IS NULL OR status = 'voided'),
    CHECK (carry_forward IS NULL OR jsonb_typeof(carry_forward) = 'object')
);

-- One active run per period. ReplaceRun voids before inserting, inside one
-- transaction, which is what lets a replacement satisfy this.
-- Multi-plan and multi-tenant scoping is HEU-506, which owns the same gap on
-- qualification_history.
CREATE UNIQUE INDEX commission_runs_active_period_idx
    ON commission_runs (period_id) WHERE status <> 'voided';

CREATE TABLE commission_results (
    id            BIGSERIAL   PRIMARY KEY,
    run_id        UUID        NOT NULL REFERENCES commission_runs(id),
    structure     TEXT        NOT NULL,
    earner_id     UUID        NOT NULL,
    -- Unrounded. DEVELOPMENT.md puts rounding to cents at the payout layer.
    -- NUMERIC rather than DOUBLE PRECISION so SUM is exact and independent of
    -- row order.
    dollar_amount NUMERIC     NOT NULL,
    detail        JSONB       NOT NULL,
    CHECK (structure <> ''),
    CHECK (dollar_amount <> 'NaN'::numeric),
    CHECK (jsonb_typeof(detail) = 'object')
);

-- Serves "this distributor's earnings in this run", the dispute query.
CREATE INDEX commission_results_run_earner_idx
    ON commission_results (run_id, earner_id);

-- Serves the per-structure DELETE in SaveResults, which is what makes a
-- retry idempotent.
CREATE INDEX commission_results_run_structure_idx
    ON commission_results (run_id, structure);
