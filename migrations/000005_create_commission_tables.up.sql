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
    -- Must reference a run for the same period. That IS expressible as a
    -- foreign key, contrary to the original design note: the composite
    -- reference below, against the UNIQUE (id, period_id) declared after the
    -- checks, forces the referenced run to carry this row's period. Since id
    -- is the primary key, exactly one row has that id, so its period_id must
    -- match. ReplaceRun still inherits period_id from the locked old row —
    -- the constraint is what stops a future writer getting it wrong.
    superseded_by UUID,
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
    -- The other side of that one-directional rule: a run still running has
    -- not completed, so it must not carry a completion time. Without this,
    -- 'running' with a completed_at is a reachable nonsense state.
    CHECK (status <> 'running' OR completed_at IS NULL),
    CHECK ((status = 'voided') = (voided_at IS NOT NULL)),
    CHECK (superseded_by IS NULL OR status = 'voided'),
    -- The composite foreign key below is blind to a self-reference: a row's
    -- own (id, period_id) trivially exists, so the same-period rule is
    -- satisfied. A cycle here would hang any walk of the supersede chain,
    -- hence a separate check.
    CHECK (superseded_by IS NULL OR superseded_by <> id),
    CHECK (carry_forward IS NULL OR jsonb_typeof(carry_forward) = 'object'),
    -- The target of the composite foreign key below. Redundant with the
    -- primary key on its own, and that is the point: it is what lets another
    -- column pair reference (id, period_id) together.
    UNIQUE (id, period_id),
    -- A replacement must live in the same period as the run it supersedes.
    FOREIGN KEY (superseded_by, period_id) REFERENCES commission_runs (id, period_id)
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
    -- Go rejects the all-zero UUID in validateResultInputs and again in the
    -- copy source, but both are bypassed by manual SQL or a future writer
    -- that does not go through the store. This is the money table's system of
    -- record, so the invariant belongs here too — the fail-loud-on-bypass-
    -- paths rule in docs/development/config-types.md.
    CHECK (earner_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    -- Every non-finite value, not just NaN. Postgres 14+ accepts Infinity in
    -- a NUMERIC column, and strconv.FormatFloat(math.Inf(1), 'f', -1, 64)
    -- emits "+Inf" — the exact text path this design uses for float64
    -- amounts. One infinity makes SUM over the run return Infinity; a mixed
    -- pair makes it NaN. Either way the run's total is destroyed, which is
    -- what the NaN guard alone failed to prevent. NaN sorts above Infinity
    -- in NUMERIC ordering, so the upper bound rejects it too.
    --
    -- Note this pins Postgres 14 or newer: 'Infinity'::numeric does not
    -- parse before 14, so the CREATE TABLE itself would fail. The test
    -- container is pinned to postgres:16-alpine.
    CHECK (dollar_amount > '-Infinity'::numeric AND dollar_amount < 'Infinity'::numeric),
    CHECK (jsonb_typeof(detail) = 'object')
);

-- Serves GetResults: WHERE run_id = $1 ORDER BY id ASC, in one index scan
-- with no sort. Without it Postgres walks the whole primary key index and
-- filters, so the cost scales with total table size rather than with the run
-- being read. Measured at 250k rows across two runs: 200k rows removed by
-- filter to return 50k. Rows are never deleted here, so that gap widens with
-- every run retained.
--
-- It does NOT serve GetLiveResults, which joins to commission_runs to resolve
-- the period's live run in one statement. A nested loop takes its ordering
-- from the outer relation, so the inner scan's (run_id, id) order cannot
-- satisfy ORDER BY r.id and the planner adds a Sort. Measured at 1.35M rows
-- with 500k live: 42 MB spilled to disk. Tracked in HEU-555's plan as an open
-- read-path question, not solved here.
CREATE INDEX commission_results_run_id_idx
    ON commission_results (run_id, id);

-- Serves "this distributor's earnings in this run", the dispute query.
CREATE INDEX commission_results_run_earner_idx
    ON commission_results (run_id, earner_id);

-- Serves the per-structure DELETE in SaveResults, which is what makes a
-- retry idempotent.
CREATE INDEX commission_results_run_structure_idx
    ON commission_results (run_id, structure);
