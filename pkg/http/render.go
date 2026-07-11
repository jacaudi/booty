package http

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
)

// TemplateVars is the single-sourced SHAPE of the render variables. It is
// populated PER FAMILY by the resolution/preview path — NOT here — because
// .ServerIP carries different semantics per family (host:port for ignition,
// host-only + .ServerHTTPPort for machineconfig); see design §6/§14-D11.
type TemplateVars struct {
	Hostname       string
	MAC            string
	IP             string
	UUID           string
	Serial         string
	ServerIP       string
	ServerHTTPPort string
	JoinString     string
	Roles          []string
	TalosVersion   string
	Schematic      string
}

// familyAllowsKind reports whether an authored config kind may serve a host of
// the given family (family ConfigKind == serving mechanism). One contract, three
// consumers; the preseed family is the only 1:many case.
func familyAllowsKind(familyConfigKind, kind string) bool {
	switch familyConfigKind {
	case "ignition":
		return kind == "butane" // author butane, serve ignition
	case "preseed":
		return kind == "preseed" || kind == "debianconfig"
	default:
		return kind == familyConfigKind // machineconfig, ...
	}
}

// renderConfig executes source as a text/template against vars, then translates
// per kind. It is the SHARED step consumed by both the serving handlers and
// POST /configs/{id}/preview. vars must already be populated by the caller.
func renderConfig(kind string, source []byte, vars TemplateVars) (out []byte, contentType, report string, err error) {
	tpl, err := template.New("config").Parse(string(source))
	if err != nil {
		return nil, "", "", fmt.Errorf("http: parse config template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, vars); err != nil {
		return nil, "", "", fmt.Errorf("http: render config template: %w", err)
	}
	rendered := buf.Bytes()

	switch kind {
	case "butane":
		ignCfg, rep, terr := butaneConfig.TranslateBytes(rendered, butaneCommon.TranslateBytesOptions{Pretty: true})
		if terr != nil {
			return nil, "", rep.String(), fmt.Errorf("http: butane translate: %w", terr)
		}
		if rep.IsFatal() {
			return nil, "", rep.String(), errors.New("http: fatal butane report: " + rep.String())
		}
		return ignCfg, "application/json", rep.String(), nil
	case "machineconfig":
		return rendered, "text/yaml", "", nil
	case "preseed":
		return rendered, "text/plain", "", nil
	default:
		return nil, "", "", fmt.Errorf("http: unknown config kind %q", kind)
	}
}
