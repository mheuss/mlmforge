CREATE TABLE qualification_history (
    period_id    TEXT        NOT NULL,
    user_id      UUID        NOT NULL,
    rank         TEXT,
    ordinal      INTEGER,
    evaluated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (period_id, user_id),
    CHECK ((rank IS NULL) = (ordinal IS NULL))
);

CREATE INDEX qualification_history_user_period_idx
    ON qualification_history (user_id, period_id);
