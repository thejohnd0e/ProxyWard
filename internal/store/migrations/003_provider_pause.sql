-- Provider-wide pauses for the speed adapter (and any later provider).
-- until_ms = 0 with a reason means "until explicitly resumed" (HTTP 403).

CREATE TABLE provider_pause (
    provider TEXT PRIMARY KEY,
    reason   TEXT    NOT NULL DEFAULT '',
    until_ms INTEGER NOT NULL DEFAULT 0,
    attempt  INTEGER NOT NULL DEFAULT 0
);
