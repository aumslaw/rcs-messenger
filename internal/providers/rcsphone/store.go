package rcsphone

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
)

var ErrNoSession = errors.New("no paired session stored")

const sessionKey = "default"

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Load(ctx context.Context) (*libgm.AuthData, error) {
	if s == nil || s.db == nil {
		return nil, ErrNoSession
	}

	var raw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT session FROM rcs_sessions WHERE id = $1`, sessionKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("reading the rcs session: %w", err)
	}

	ad := libgm.NewAuthData()
	if err := json.Unmarshal(raw, ad); err != nil {
		return nil, fmt.Errorf("stored rcs session is corrupt: %w", err)
	}
	return ad, nil
}

func (s *Store) Save(ctx context.Context, ad *libgm.AuthData) error {
	if s == nil || s.db == nil {
		return errors.New("no database to store the rcs session in")
	}

	raw, err := json.Marshal(ad)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO rcs_sessions (id, session, updated_at)
		VALUES ($1, $2, EXTRACT(EPOCH FROM now()) * 1000)
		ON CONFLICT (id) DO UPDATE
		SET session = EXCLUDED.session, updated_at = EXCLUDED.updated_at`,
		sessionKey, raw)
	if err != nil {
		return fmt.Errorf("storing the rcs session: %w", err)
	}
	return nil
}

func (s *Store) Clear(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM rcs_sessions WHERE id = $1`, sessionKey)
	return err
}

func (s *Store) Paired(ctx context.Context) bool {
	_, err := s.Load(ctx)
	return err == nil
}
