package db

import (
	"database/sql"
	"errors"
	"testing"
)

func TestMeta_SetGetUpsert(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.GetMeta("schema"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetMeta miss: err = %v, want sql.ErrNoRows", err)
	}
	if err := s.SetMeta("schema", "1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, err := s.GetMeta("schema")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if got != "1" {
		t.Errorf("GetMeta = %q, want 1", got)
	}
	if err := s.SetMeta("schema", "2"); err != nil {
		t.Fatalf("SetMeta update: %v", err)
	}
	if got, _ := s.GetMeta("schema"); got != "2" {
		t.Errorf("GetMeta after update = %q, want 2", got)
	}
}
