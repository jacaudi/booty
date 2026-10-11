package nodereport

import (
	"encoding/json"
	"fmt"

	"github.com/coreos/ignition/v2/config"
	"github.com/vincent-petithory/dataurl"
)

// scriptMode is 0755 in decimal, as Ignition JSON requires.
const scriptMode = 0o755

// MergeIgnition adds the health-report script and its enabled unit to an
// Ignition JSON config. An entry the config already defines at ScriptPath
// or UnitName is the operator's and is kept untouched. The result is
// validated with the Ignition parser.
func MergeIgnition(cfg []byte, script, unit string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return nil, fmt.Errorf("parse ignition config: %w", err)
	}
	storage := section(doc, "storage")
	files, _ := storage["files"].([]any)
	if !hasEntry(files, "path", ScriptPath) {
		files = append(files, map[string]any{
			"path":      ScriptPath,
			"mode":      scriptMode,
			"overwrite": true,
			"contents":  map[string]any{"source": dataurl.EncodeBytes([]byte(script))},
		})
	}
	storage["files"] = files

	systemd := section(doc, "systemd")
	units, _ := systemd["units"].([]any)
	if !hasEntry(units, "name", UnitName) {
		units = append(units, map[string]any{"name": UnitName, "enabled": true, "contents": unit})
	}
	systemd["units"] = units

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal ignition config: %w", err)
	}
	_, report, perr := config.Parse(out)
	if perr != nil {
		return nil, fmt.Errorf("merged ignition config invalid: %w: %s", perr, report.String())
	}
	if report.IsFatal() {
		return nil, fmt.Errorf("merged ignition config invalid: %s", report.String())
	}
	return out, nil
}

// section returns doc[key] as an object, creating it when absent.
func section(doc map[string]any, key string) map[string]any {
	m, ok := doc[key].(map[string]any)
	if !ok {
		m = map[string]any{}
		doc[key] = m
	}
	return m
}

// hasEntry reports whether any object in list has field == want.
func hasEntry(list []any, field, want string) bool {
	for _, e := range list {
		if m, ok := e.(map[string]any); ok && m[field] == want {
			return true
		}
	}
	return false
}
