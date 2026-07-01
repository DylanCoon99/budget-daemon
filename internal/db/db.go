package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func Open(dbPath string) (*sql.DB, error) {
	// Ensure the directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}

func runMigrations(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS institutions (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    source_type     TEXT NOT NULL,
    plaid_item_id   TEXT,
    access_token    BLOB,
    sync_cursor     TEXT,
    ofx_url         TEXT,
    last_synced_at  DATETIME,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS accounts (
    id              TEXT PRIMARY KEY,
    institution_id  TEXT NOT NULL REFERENCES institutions(id),
    external_id     TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    account_type    TEXT NOT NULL,
    current_balance REAL,
    available_balance REAL,
    currency        TEXT NOT NULL DEFAULT 'USD',
    is_active       INTEGER NOT NULL DEFAULT 1,
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS categories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    parent_id   INTEGER REFERENCES categories(id),
    is_system   INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Seed system categories (ignore if already exist)
INSERT OR IGNORE INTO categories (name, is_system) VALUES
    ('Groceries', 1), ('Dining Out', 1), ('Gas & Fuel', 1),
    ('Utilities', 1), ('Rent/Mortgage', 1), ('Insurance', 1),
    ('Subscriptions', 1), ('Entertainment', 1), ('Shopping', 1),
    ('Healthcare', 1), ('Transportation', 1), ('Travel', 1),
    ('Income', 1), ('Transfer', 1), ('Investment', 1),
    ('Fees & Interest', 1), ('ATM/Cash', 1), ('Uncategorized', 1);

CREATE TABLE IF NOT EXISTS transactions (
    id                      TEXT PRIMARY KEY,
    external_id             TEXT NOT NULL UNIQUE,
    account_id              TEXT NOT NULL REFERENCES accounts(id),
    category_id             INTEGER REFERENCES categories(id),
    date                    DATE NOT NULL,
    amount                  REAL NOT NULL,
    description             TEXT NOT NULL,
    merchant_name           TEXT,
    is_pending              INTEGER NOT NULL DEFAULT 0,
    ai_category_confidence  REAL,
    ai_categorized_at       DATETIME,
    user_overridden         INTEGER NOT NULL DEFAULT 0,
    notes                   TEXT,
    raw_metadata            TEXT,
    created_at              DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at              DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_transactions_date ON transactions(date);
CREATE INDEX IF NOT EXISTS idx_transactions_account ON transactions(account_id);
CREATE INDEX IF NOT EXISTS idx_transactions_category ON transactions(category_id);

CREATE TABLE IF NOT EXISTS budget_rules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    rule_type       TEXT NOT NULL,
    category_id     INTEGER REFERENCES categories(id),
    threshold       REAL NOT NULL,
    period          TEXT NOT NULL DEFAULT 'monthly',
    notify_at_pct   TEXT NOT NULL DEFAULT '80,100',
    notify_channel  TEXT NOT NULL DEFAULT 'sms',
    is_active       INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS alert_history (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id         INTEGER NOT NULL REFERENCES budget_rules(id),
    period_start    DATE NOT NULL,
    threshold_pct   INTEGER NOT NULL,
    current_amount  REAL NOT NULL,
    message         TEXT NOT NULL,
    channel         TEXT NOT NULL,
    sent_at         DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE (rule_id, period_start, threshold_pct)
);

CREATE TABLE IF NOT EXISTS categorization_overrides (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    description_pattern TEXT NOT NULL,
    category_id         INTEGER NOT NULL REFERENCES categories(id),
    is_regex            INTEGER NOT NULL DEFAULT 0,
    priority            INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS monthly_rollups (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    year_month          TEXT NOT NULL,
    account_id          TEXT REFERENCES accounts(id),
    category_id         INTEGER REFERENCES categories(id),
    transaction_count   INTEGER NOT NULL,
    total_amount        REAL NOT NULL,
    avg_amount          REAL NOT NULL,
    UNIQUE (year_month, account_id, category_id)
);
`
