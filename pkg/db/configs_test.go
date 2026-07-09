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
	revID, rev, err := s.AddConfigRevision(id, "c291cmNl", "abc123", nil)
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
		_, rev, err := s.AddConfigRevision(id, "eA==", "h", nil)
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
	r1, _, _ := s.AddConfigRevision(id, "djE=", "h1", nil)
	r2, _, _ := s.AddConfigRevision(id, "djI=", "h2", nil)
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
	_, rev, _ := s.AddConfigRevision(id, "djM=", "h3", nil)
	if rev != 3 {
		t.Fatalf("edit-after-rollback revision = %d, want 3", rev)
	}
}

func TestGetRevisionByNumber(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("c", "butane")
	s.AddConfigRevision(id, "djE=", "h1", nil)
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
		rid, _, _ := s.AddConfigRevision(id, "eA==", "h", nil)
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
	s.AddConfigRevision(id, "eA==", "h", nil)
	if _, err := s.db.Exec(`DELETE FROM configs WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM config_revisions WHERE config_id = ?`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("config_revisions after config delete = %d, want 0 (cascade)", n)
	}
}

func TestAddConfigRevisionDerivedSchematicID(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateConfig("iscsi", "schematic")
	if err != nil {
		t.Fatalf("CreateConfig(schematic): %v", err)
	}
	want := "a1b2c3d4"
	revID, rev, err := s.AddConfigRevision(id, "Y3VzdG9taXphdGlvbjoge30K", "h1", &want)
	if err != nil || rev != 1 {
		t.Fatalf("AddConfigRevision = rev %d, err %v", rev, err)
	}
	if err := s.SetActiveRevision(id, revID); err != nil {
		t.Fatal(err)
	}
	for name, get := range map[string]func() (*ConfigRevision, error){
		"GetActiveRevision": func() (*ConfigRevision, error) { return s.GetActiveRevision(id) },
		"GetRevision":       func() (*ConfigRevision, error) { return s.GetRevision(id, 1) },
	} {
		r, gerr := get()
		if gerr != nil {
			t.Fatalf("%s: %v", name, gerr)
		}
		if r.DerivedSchematicID == nil || *r.DerivedSchematicID != want {
			t.Fatalf("%s DerivedSchematicID = %v, want %q", name, r.DerivedSchematicID, want)
		}
	}
	revs, err := s.ListRevisions(id)
	if err != nil || len(revs) != 1 || revs[0].DerivedSchematicID == nil || *revs[0].DerivedSchematicID != want {
		t.Fatalf("ListRevisions = %+v, err %v", revs, err)
	}
}

func TestAddConfigRevisionNilDerivedStaysNull(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateConfig("plain", "butane")
	revID, _, err := s.AddConfigRevision(id, "eA==", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetActiveRevision(id, revID)
	r, err := s.GetActiveRevision(id)
	if err != nil || r.DerivedSchematicID != nil {
		t.Fatalf("non-schematic revision DerivedSchematicID = %v (err %v), want nil", r.DerivedSchematicID, err)
	}
}

func TestListConfigsDerivedSchematicID(t *testing.T) {
	s := newTestStore(t)
	sid, _ := s.CreateConfig("sch", "schematic")
	want := "a1b2c3d4"
	rid, _, _ := s.AddConfigRevision(sid, "Y3VzdG9taXphdGlvbjoge30K", "h", &want)
	s.SetActiveRevision(sid, rid)
	s.CreateConfig("plain", "butane") // no active revision

	rows, err := s.ListConfigs()
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListConfigs = %d rows, err %v", len(rows), err)
	}
	for _, r := range rows {
		switch r.Name {
		case "sch":
			if r.DerivedSchematicID != want {
				t.Errorf("sch DerivedSchematicID = %q, want %q", r.DerivedSchematicID, want)
			}
		case "plain":
			if r.DerivedSchematicID != "" {
				t.Errorf("plain DerivedSchematicID = %q, want empty", r.DerivedSchematicID)
			}
		}
	}
}
