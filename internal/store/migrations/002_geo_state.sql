-- Geolocation provider state: daily attempt budget and pauses survive restarts.
-- budget_day is the UTC date (YYYY-MM-DD) that budget_used counts for.
-- pause_until is Unix milliseconds; 0 with a pause_reason means "until the key changes".

CREATE TABLE geo_state (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    budget_day      TEXT    NOT NULL DEFAULT '',
    budget_used     INTEGER NOT NULL DEFAULT 0,
    pause_until     INTEGER NOT NULL DEFAULT 0,
    pause_reason    TEXT    NOT NULL DEFAULT '',
    backoff_attempt INTEGER NOT NULL DEFAULT 0,
    key_fingerprint TEXT    NOT NULL DEFAULT ''
);
INSERT INTO geo_state (id) VALUES (1);
