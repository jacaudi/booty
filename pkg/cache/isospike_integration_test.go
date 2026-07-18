//go:build isospike

// Package cache: Step-0 library-validation spike for
// github.com/diskfs/go-diskfs (task-6-brief.md Step 0). This is NOT part of
// the normal test suite — it is excluded from `go build ./...`/`go test
// ./...` by the isospike build tag. It is a manual runbook for validating
// diskfs's Rock Ridge support against a REAL Debian DVD-1 ISO, deferred to
// the netboot lab and bundled with Task 12 — this session has no ISO to run
// it against, so it has NOT been executed; only compile-checked.
//
// Run once a real ISO is available:
//
//	BOOTY_TEST_DVD_ISO=/path/to/debian-12.x.y-amd64-DVD-1.iso \
//	  go test -tags isospike ./pkg/cache/ -run ISOSpike -v
//
// PASS -> diskfs/go-diskfs correctly reads deep, case-sensitive pool/ paths
// via Rock Ridge; the provisional adoption in extractAndMergeISO/extractISO
// (debiandvd.go) is confirmed.
//
// FAIL (Rock Ridge deep paths truncated/missing) -> do NOT use diskfs.
// Fallback order (task-6-brief.md Step 0):
//  1. github.com/kdomanski/iso9660's read path.
//  2. Last resort: shell out to `xorriso -osirrox on` / `bsdtar -xf`, which
//     adds a runtime binary dependency and breaks the distroless/pure-Go
//     posture — requires a design amendment + user sign-off before use.
package cache

import (
	"os"
	"strings"
	"testing"

	"github.com/diskfs/go-diskfs"
)

func TestISOSpike_RockRidgeDeepPathRead(t *testing.T) {
	isoPath := os.Getenv("BOOTY_TEST_DVD_ISO")
	if isoPath == "" {
		t.Skip("BOOTY_TEST_DVD_ISO not set; this spike requires a real Debian DVD-1 ISO (see file doc comment)")
	}

	d, err := diskfs.Open(isoPath, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("open ISO %s: %v", isoPath, err)
	}
	defer d.Close()

	fsys, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatalf("read ISO9660 filesystem: %v", err)
	}

	const deepDir = "pool/main/l/linux"
	entries, err := fsys.ReadDir(deepDir)
	if err != nil {
		t.Fatalf("read deep Rock Ridge path %s: %v", deepDir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("%s is empty on the real DVD-1 ISO", deepDir)
	}

	var debName string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".deb") {
			debName = e.Name()
			break
		}
	}
	if debName == "" {
		t.Fatalf("no .deb file found under %s", deepDir)
	}

	// A truncated ISO9660 level-1 (non-Rock-Ridge) name would be an 8.3
	// short name (e.g. "LINUX123.DEB;1"), not a full versioned Debian
	// package filename. These checks fail if Rock Ridge NM records were
	// not applied.
	if strings.Contains(debName, ";") {
		t.Fatalf("filename %q looks like a raw ISO9660 level-1 name (Rock Ridge NM not applied)", debName)
	}
	if len(debName) <= 12 {
		t.Fatalf("filename %q is short enough to be an untruncated 8.3 ISO9660 name (Rock Ridge NM not applied)", debName)
	}

	data, err := fsys.ReadFile(deepDir + "/" + debName)
	if err != nil {
		t.Fatalf("read %s/%s: %v", deepDir, debName, err)
	}
	if len(data) == 0 {
		t.Fatalf("%s/%s read zero bytes", deepDir, debName)
	}
	t.Logf("read %s/%s: long, case-preserved Rock Ridge name OK (%d bytes)", deepDir, debName, len(data))
}
