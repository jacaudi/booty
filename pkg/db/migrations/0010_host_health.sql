CREATE TABLE host_health (
    mac         TEXT PRIMARY KEY REFERENCES hosts(mac) ON DELETE CASCADE,
    received_at TEXT NOT NULL,
    report      TEXT NOT NULL
);
ALTER TABLE hosts ADD COLUMN report_token_gen INTEGER NOT NULL DEFAULT 0;
