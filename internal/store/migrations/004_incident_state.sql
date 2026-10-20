-- Failure streaks for incident tracking, and one outbox row per event.

CREATE TABLE proxy_state (
    proxy_id       INTEGER PRIMARY KEY REFERENCES proxies (id) ON DELETE CASCADE,
    failure_streak INTEGER NOT NULL DEFAULT 0,
    updated_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX outbox_one_per_event ON notification_outbox (event_id) WHERE event_id IS NOT NULL;
