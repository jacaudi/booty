package db

import "fmt"

// GetMeta returns the value for key, or sql.ErrNoRows if absent.
func (s *Store) GetMeta(key string) (string, error) {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// SetMeta inserts or replaces the value for key.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("db: set meta %q: %w", key, err)
	}
	return nil
}
