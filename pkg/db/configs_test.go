package db

import (
	"errors"
	"testing"
)

func TestCreateConfigAndFirstRevision(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateConfig("prod-butane", "butane")
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	revID, rev, err := s.AddConfigRevision(id, "c291cmNl", "abc123")
	if err != nil {
		t.Fatalf("AddConfigRevision: %v", err)
	}
	if rev != 1 {
		t.Fatalf("first revision = %d, want 1", rev)
	}
	if err := s.SetActiveRevision(id, revID); err != nil {
		t.Fatalf("SetActiveRevision: %v", err)
	}
	c, err := s.GetConfig(id)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if c.Name != "prod-butane" || c.Kind != "butane" || !c.ActiveRevisionID.Valid || c.ActiveRevisionID.Int64 != revID {
		t.Fatalf("GetConfig = %+v, mismatch", c)
	}
}

func TestGetConfigNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetConfig(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetConfig(missing) err = %v, want ErrNotFound", err)
	}
}

func TestAddConfigRevisionMonotonic(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("c", "butane")
	for want := 1; want <= 3; want++ {
		_, rev, err := s.AddConfigRevision(id, "eA==", "h")
		if err != nil {
			t.Fatalf("AddConfigRevision: %v", err)
		}
		if rev != want {
			t.Fatalf("revision = %d, want %d (per-config monotonic)", rev, want)
		}
	}
	if n, _ := s.CountRevisions(id); n != 3 {
		t.Fatalf("CountRevisions = %d, want 3", n)
	}
}

func TestRollbackIsPointerMoveNoNewRevision(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("c", "butane")
	r1, _, _ := s.AddConfigRevision(id, "djE=", "h1")
	r2, _, _ := s.AddConfigRevision(id, "djI=", "h2")
	if err := s.SetActiveRevision(id, r2); err != nil {
		t.Fatal(err)
	}
	// Roll back to revision 1: active pointer moves, NO new revision row.
	if err := s.SetActiveRevision(id, r1); err != nil {
		t.Fatalf("rollback SetActiveRevision: %v", err)
	}
	if n, _ := s.CountRevisions(id); n != 2 {
		t.Fatalf("rollback must not add a revision: count = %d, want 2", n)
	}
	c, _ := s.GetConfig(id)
	if c.ActiveRevisionID.Int64 != r1 {
		t.Fatalf("active = %d, want %d (rolled back)", c.ActiveRevisionID.Int64, r1)
	}
	// A subsequent edit branches forward as max+1 (=3), not 2.
	_, rev, _ := s.AddConfigRevision(id, "djM=", "h3")
	if rev != 3 {
		t.Fatalf("edit-after-rollback revision = %d, want 3", rev)
	}
}

func TestGetRevisionByNumber(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("c", "butane")
	s.AddConfigRevision(id, "djE=", "h1")
	got, err := s.GetRevision(id, 1)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if got.SourceB64 != "djE=" {
		t.Fatalf("GetRevision source = %q, want djE=", got.SourceB64)
	}
	if _, err := s.GetRevision(id, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRevision(absent) err = %v, want ErrNotFound", err)
	}
}

func TestPruneKeepsNewestNUnionActive(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("c", "butane")
	var revIDs []int64
	for range 5 {
		rid, _, _ := s.AddConfigRevision(id, "eA==", "h")
		revIDs = append(revIDs, rid)
	}
	// Roll active back to the OLDEST (revision 1), then prune keep=2. The union
	// must protect the active row so it is never evicted (the data-loss hazard).
	if err := s.SetActiveRevision(id, revIDs[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneRevisions(id, 2); err != nil {
		t.Fatalf("PruneRevisions: %v", err)
	}
	// Kept: newest 2 by revision (4,5) UNION active (1) = 3 rows.
	revs, _ := s.ListRevisions(id)
	if len(revs) != 3 {
		t.Fatalf("kept %d revisions, want 3 (newest-2 ∪ active)", len(revs))
	}
	if _, err := s.GetRevision(id, 1); err != nil {
		t.Fatalf("active revision 1 must survive prune: %v", err)
	}
	for _, r := range revs {
		if r.Revision == 2 || r.Revision == 3 {
			t.Fatalf("revision %d should have been pruned", r.Revision)
		}
	}
}

func TestDeleteConfigCascadesRevisions(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("c", "butane")
	s.AddConfigRevision(id, "eA==", "h")
	if _, err := s.db.Exec(`DELETE FROM configs WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM config_revisions WHERE config_id = ?`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("config_revisions after config delete = %d, want 0 (cascade)", n)
	}
}
