-- Initial schema. Timestamps are Unix milliseconds in UTC; durations are
-- microseconds; sizes are bytes.

CREATE TABLE proxies (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    protocol           TEXT    NOT NULL CHECK (protocol IN ('http', 'https', 'socks5')),
    host               TEXT    NOT NULL CHECK (length(host) > 0),
    port               INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    auth_enabled       INTEGER NOT NULL DEFAULT 0 CHECK (auth_enabled IN (0, 1)),
    username           TEXT    NOT NULL DEFAULT '',
    password_enc       BLOB,
    credential_digest  TEXT    NOT NULL DEFAULT '',
    tags               TEXT    NOT NULL DEFAULT '[]',
    availability_enabled     INTEGER CHECK (availability_enabled IN (0, 1)),
    availability_interval_s  INTEGER CHECK (availability_interval_s BETWEEN 60 AND 604800),
    speed_enabled            INTEGER CHECK (speed_enabled IN (0, 1)),
    speed_interval_s         INTEGER CHECK (speed_interval_s BETWEEN 60 AND 604800),
    next_availability_at     INTEGER,
    next_speed_at            INTEGER,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX proxies_identity
    ON proxies (protocol, lower(host), port, username, credential_digest);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    secret     INTEGER NOT NULL DEFAULT 0 CHECK (secret IN (0, 1)),
    updated_at INTEGER NOT NULL
);

CREATE TABLE admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT    NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_digest TEXT PRIMARY KEY,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    proxy_id    INTEGER NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('availability', 'speed')),
    status      TEXT    NOT NULL CHECK (status IN ('pending', 'running', 'done', 'canceled', 'interrupted')),
    source      TEXT    NOT NULL CHECK (source IN ('manual', 'scheduled')),
    due_at      INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
);
CREATE INDEX jobs_due ON jobs (status, due_at);
CREATE UNIQUE INDEX jobs_one_active
    ON jobs (proxy_id, kind) WHERE status IN ('pending', 'running');

CREATE TABLE availability_results (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    proxy_id        INTEGER NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    checked_at      INTEGER NOT NULL,
    state           TEXT    NOT NULL CHECK (state IN ('unknown', 'up', 'down', 'indeterminate')),
    exit_ip         TEXT    NOT NULL DEFAULT '',
    endpoint        TEXT    NOT NULL DEFAULT '',
    duration_us     INTEGER NOT NULL DEFAULT 0,
    failure_code    TEXT    NOT NULL DEFAULT '',
    failure_message TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX availability_history ON availability_results (proxy_id, checked_at);

CREATE TABLE speed_results (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    proxy_id            INTEGER NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    checked_at          INTEGER NOT NULL,
    provider            TEXT    NOT NULL,
    method              TEXT    NOT NULL,
    download_bytes      INTEGER NOT NULL DEFAULT 0,
    download_us         INTEGER NOT NULL DEFAULT 0,
    download_mbps       REAL,
    download_low_conf   INTEGER NOT NULL DEFAULT 0,
    download_fail_code  TEXT    NOT NULL DEFAULT '',
    download_fail_msg   TEXT    NOT NULL DEFAULT '',
    upload_bytes        INTEGER NOT NULL DEFAULT 0,
    upload_us           INTEGER NOT NULL DEFAULT 0,
    upload_mbps         REAL,
    upload_low_conf     INTEGER NOT NULL DEFAULT 0,
    upload_fail_code    TEXT    NOT NULL DEFAULT '',
    upload_fail_msg     TEXT    NOT NULL DEFAULT '',
    total_bytes         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX speed_history ON speed_results (proxy_id, checked_at);

CREATE TABLE geo_cache (
    ip           TEXT PRIMARY KEY,
    country_code TEXT NOT NULL DEFAULT '',
    country      TEXT NOT NULL DEFAULT '',
    city         TEXT NOT NULL DEFAULT '',
    asn          INTEGER,
    organization TEXT NOT NULL DEFAULT '',
    fetched_at   INTEGER NOT NULL
);

CREATE TABLE incidents (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    proxy_id             INTEGER NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    started_at           INTEGER NOT NULL,
    ended_at             INTEGER,
    consecutive_failures INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX incidents_one_open ON incidents (proxy_id) WHERE ended_at IS NULL;

CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    proxy_id   INTEGER REFERENCES proxies (id) ON DELETE SET NULL,
    kind       TEXT    NOT NULL,
    message    TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX events_created ON events (created_at, id);
CREATE INDEX events_proxy ON events (proxy_id, created_at);

CREATE TABLE notification_outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        INTEGER REFERENCES events (id) ON DELETE SET NULL,
    payload         TEXT    NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    delivered_at    INTEGER,
    last_error      TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);
CREATE INDEX outbox_due ON notification_outbox (delivered_at, next_attempt_at);
