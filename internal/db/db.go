package db

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
)

var schema = []string{
	`CREATE TABLE IF NOT EXISTS rcs_sessions (
		id         text   PRIMARY KEY,
		session    bytea  NOT NULL,
		updated_at bigint NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS rcs_send_results (
		id              BIGSERIAL PRIMARY KEY,
		phone           TEXT NOT NULL,
		conversation_id TEXT NOT NULL DEFAULT '',
		message_id      TEXT NOT NULL DEFAULT '',
		outcome         TEXT NOT NULL,
		status          TEXT NOT NULL DEFAULT '',
		transport       TEXT NOT NULL DEFAULT '',
		e2ee_before     TEXT NOT NULL DEFAULT '',
		e2ee_after      TEXT NOT NULL DEFAULT '',
		e2ee_evidence   TEXT NOT NULL DEFAULT '',
		capability      TEXT NOT NULL DEFAULT '',
		detail          TEXT NOT NULL DEFAULT '',
		created_at      BIGINT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_rcs_send_results_phone ON rcs_send_results (phone, created_at DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_rcs_send_results_created ON rcs_send_results (created_at DESC);`,
}

func Open(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	for i, stmt := range schema {
		if _, err := conn.Exec(stmt); err != nil {
			conn.Close()
			return nil, fmt.Errorf("schema statement %d: %w", i, err)
		}
	}
	return conn, nil
}
