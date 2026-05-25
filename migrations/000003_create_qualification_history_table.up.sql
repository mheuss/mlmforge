-- Defense in depth: the Go contract enforces these invariants, but adding them
-- here stops bad rows from any other writer (manual SQL, future code path).
-- ordinal range pins the Go-side uint16 mapping; 0 is excluded because the
-- mapper rejects qualified-with-zero-ordinal as invalid.
CREATE TABLE qualification_history (
    period_id    TEXT        NOT NULL,
    user_id      UUID        NOT NULL,
    rank         TEXT,
    ordinal      INTEGER,
    evaluated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (period_id, user_id),
    CHECK (period_id <> ''),
    CHECK ((rank IS NULL) = (ordinal IS NULL)),
    CHECK (ordinal IS NULL OR (ordinal BETWEEN 1 AND 65535))
);

CREATE INDEX qualification_history_user_period_idx
    ON qualification_history (user_id, period_id);
