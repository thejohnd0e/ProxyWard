ALTER TABLE proxies ADD COLUMN monitoring_paused INTEGER NOT NULL DEFAULT 0 CHECK (monitoring_paused IN (0, 1));
