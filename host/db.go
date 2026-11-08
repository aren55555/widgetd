package host

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS kv (
	instance TEXT NOT NULL,
	key      TEXT NOT NULL,
	value    BLOB NOT NULL,
	PRIMARY KEY (instance, key)
);
CREATE TABLE IF NOT EXISTS admins (
	id         BLOB PRIMARY KEY,   -- WebAuthn user handle
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS credentials (
	id         BLOB PRIMARY KEY,   -- credential id
	admin_id   BLOB NOT NULL REFERENCES admins(id),
	name       TEXT NOT NULL,
	data       BLOB NOT NULL,      -- webauthn.Credential, JSON
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash BLOB PRIMARY KEY,   -- sha256 of the cookie value
	role       INTEGER NOT NULL,   -- widgetdv1.Role
	subject    TEXT NOT NULL,      -- admin id (hex) or device id
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	paired_at  INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS instances (
	id         TEXT PRIMARY KEY,
	widget     TEXT NOT NULL,
	label      TEXT NOT NULL DEFAULT '',
	config     BLOB,              -- the widget's Config, encoded; NULL until configured
	configured INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS secrets (
	instance TEXT NOT NULL,
	field    TEXT NOT NULL,
	value    BLOB NOT NULL,     -- sealed: see secrets.go
	PRIMARY KEY (instance, field)
);
CREATE TABLE IF NOT EXISTS sign_in_clients (
	widget        TEXT PRIMARY KEY,
	client_id     TEXT NOT NULL,
	client_secret BLOB              -- sealed; NULL if none
);
CREATE TABLE IF NOT EXISTS tokens (
	instance TEXT PRIMARY KEY,
	value    BLOB NOT NULL          -- sealed JSON: see signin.go
);
CREATE TABLE IF NOT EXISTS grants (
	widget   TEXT PRIMARY KEY,
	disabled INTEGER NOT NULL DEFAULT 0,
	hosts    TEXT               -- JSON array; NULL grants everything requested
);
`

// maxKVValue and maxKVKey bound what one instance can store per key.
const (
	maxKVKey   = 256
	maxKVValue = 256 << 10
)

func openDB(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "widgetd.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// instanceKV is one instance's slice of the kv table. The instance id comes
// from the host, never from the widget, so a widget can only ever name keys
// within its own slice.
type instanceKV struct {
	db       *sql.DB
	instance string
}

func (k instanceKV) Get(key string) ([]byte, bool, error) {
	var v []byte
	err := k.db.QueryRow(`SELECT value FROM kv WHERE instance = ? AND key = ?`, k.instance, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return v, err == nil, err
}

func (k instanceKV) Put(key string, value []byte) error {
	if len(key) == 0 || len(key) > maxKVKey {
		return fmt.Errorf("kv key must be 1..%d bytes", maxKVKey)
	}
	if len(value) > maxKVValue {
		return fmt.Errorf("kv value is %d bytes, over the %d limit", len(value), maxKVValue)
	}
	_, err := k.db.Exec(`INSERT INTO kv (instance, key, value) VALUES (?, ?, ?)
		ON CONFLICT (instance, key) DO UPDATE SET value = excluded.value`, k.instance, key, value)
	return err
}

func (k instanceKV) Delete(key string) error {
	_, err := k.db.Exec(`DELETE FROM kv WHERE instance = ? AND key = ?`, k.instance, key)
	return err
}
