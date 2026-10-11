package nodereport

import (
	"encoding/json"
	"testing"

	"github.com/coreos/ignition/v2/config"
	"github.com/vincent-petithory/dataurl"
)

type parsedFile struct {
	Path      string `json:"path"`
	Mode      int    `json:"mode"`
	Overwrite *bool  `json:"overwrite"`
	Contents  struct {
		Source string `json:"source"`
	} `json:"contents"`
}

type parsedUnit struct {
	Name     string `json:"name"`
	Enabled  *bool  `json:"enabled"`
	Contents string `json:"contents"`
}

type parsedCfg struct {
	Storage struct {
		Files []parsedFile `json:"files"`
	} `json:"storage"`
	Systemd struct {
		Units []parsedUnit `json:"units"`
	} `json:"systemd"`
}

func mergeAndParse(t *testing.T, base, script, unit string) parsedCfg {
	t.Helper()
	out, err := MergeIgnition([]byte(base), script, unit)
	if err != nil {
		t.Fatal(err)
	}
	_, report, perr := config.Parse(out)
	if perr != nil || report.IsFatal() {
		t.Fatalf("merged config invalid: %v %s", perr, report.String())
	}
	var c parsedCfg
	if uerr := json.Unmarshal(out, &c); uerr != nil {
		t.Fatal(uerr)
	}
	return c
}

func TestMergeIgnitionAddsFileAndUnit(t *testing.T) {
	c := mergeAndParse(t, `{"ignition":{"version":"3.4.0"}}`, "#!/bin/bash\necho hi\n", "[Unit]\nDescription=x\n")
	if len(c.Storage.Files) != 1 || c.Storage.Files[0].Path != ScriptPath || c.Storage.Files[0].Mode != 493 {
		t.Fatalf("files = %+v", c.Storage.Files)
	}
	du, err := dataurl.DecodeString(c.Storage.Files[0].Contents.Source)
	if err != nil || string(du.Data) != "#!/bin/bash\necho hi\n" {
		t.Fatalf("decode: %v %q", err, du)
	}
	if len(c.Systemd.Units) != 1 || c.Systemd.Units[0].Name != UnitName ||
		c.Systemd.Units[0].Enabled == nil || !*c.Systemd.Units[0].Enabled ||
		c.Systemd.Units[0].Contents != "[Unit]\nDescription=x\n" {
		t.Fatalf("units = %+v", c.Systemd.Units)
	}
}

func TestMergeIgnitionKeepsOperatorEntries(t *testing.T) {
	base := `{"ignition":{"version":"3.4.0"},` +
		`"storage":{"files":[{"path":"` + ScriptPath + `","contents":{"source":"data:,mine"}}]},` +
		`"systemd":{"units":[{"name":"` + UnitName + `","enabled":false,"contents":"mine"}]}}`
	c := mergeAndParse(t, base, "script", "unit")
	if len(c.Storage.Files) != 1 || c.Storage.Files[0].Contents.Source != "data:,mine" {
		t.Fatalf("files = %+v", c.Storage.Files)
	}
	if len(c.Systemd.Units) != 1 || c.Systemd.Units[0].Contents != "mine" ||
		c.Systemd.Units[0].Enabled == nil || *c.Systemd.Units[0].Enabled {
		t.Fatalf("units = %+v", c.Systemd.Units)
	}
}

func TestMergeIgnitionKeepsOtherFiles(t *testing.T) {
	base := `{"ignition":{"version":"3.3.0"},"storage":{"files":[{"path":"/x","contents":{"source":"data:,x"}}]}}`
	c := mergeAndParse(t, base, "script", "unit")
	if len(c.Storage.Files) != 2 || c.Storage.Files[0].Path != "/x" || c.Storage.Files[1].Path != ScriptPath {
		t.Fatalf("files = %+v", c.Storage.Files)
	}
}

func TestMergeIgnitionInvalidJSON(t *testing.T) {
	if _, err := MergeIgnition([]byte("{not json"), "s", "u"); err == nil {
		t.Fatal("want error")
	}
}

func TestMergeIgnitionNotAnObject(t *testing.T) {
	for _, in := range []string{"null", "[]", `"x"`, "1"} {
		if _, err := MergeIgnition([]byte(in), "s", "u"); err == nil {
			t.Errorf("%s: want error", in)
		}
	}
}
