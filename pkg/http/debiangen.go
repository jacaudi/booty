package http

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"strings"
	"text/template"

	yaml "go.yaml.in/yaml/v4"
)

// debianConfigSpec is the curated debianconfig authoring schema (design §4):
// a structured YAML booty translates into a flat d-i preseed. Unset fields
// emit NO preseed line (emit-only-what-is-set) — booty never fabricates
// opinions the operator did not express; d-i defaults/prompts apply instead.
type debianConfigSpec struct {
	Hostname string          `yaml:"hostname"`
	Domain   string          `yaml:"domain"`
	Locale   string          `yaml:"locale"`
	Timezone string          `yaml:"timezone"`
	Keyboard string          `yaml:"keyboard"`
	Mirror   *debianMirror   `yaml:"mirror"`
	Network  *debianNetwork  `yaml:"network"`
	Accounts *debianAccounts `yaml:"accounts"`
	Packages []string        `yaml:"packages"`
}

// debianMirror is override-only apt mirror selection; the suite/codename comes
// from the Debian target's channel, never from here.
type debianMirror struct {
	Hostname  string `yaml:"hostname"`
	Directory string `yaml:"directory"`
	Proxy     string `yaml:"proxy"`
}

type debianNetwork struct {
	Interface string        `yaml:"interface"` // "auto" | a named iface
	Static    *debianStatic `yaml:"static"`    // omit -> DHCP
}

type debianStatic struct {
	Address     string   `yaml:"address"`
	Netmask     string   `yaml:"netmask"`
	Gateway     string   `yaml:"gateway"`
	Nameservers []string `yaml:"nameservers"`
}

// debianAccounts takes password HASHES ONLY ($6$..., pre-computed crypt) —
// booty never accepts plaintext and never generates hashes (design §10).
// An omitted root_password_hash (within a present accounts block) disables
// root login, the safer default.
type debianAccounts struct {
	RootPasswordHash string      `yaml:"root_password_hash"`
	User             *debianUser `yaml:"user"`
}

type debianUser struct {
	Fullname     string `yaml:"fullname"` // optional; falls back to username
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

// preseedView is the flattened emission view derived from a validated spec so
// the template below stays linear formatting — presence checks only, no logic.
type preseedView struct {
	Locale, Keyboard, Iface                     string
	Static                                      bool
	StaticAddress, StaticNetmask, StaticGateway string
	StaticNameservers                           string
	Hostname, Domain                            string
	MirrorManual                                bool
	MirrorHost, MirrorDir, MirrorProxy          string
	Timezone                                    string
	HasAccounts                                 bool
	RootHash                                    string
	HasUser                                     bool
	UserFullname, Username, UserHash            string
	Packages                                    string
}

// buildPreseedView validates spec coherence and derives the emission view.
// Validation errors surface as 422 through validateConfigSource's default arm.
func buildPreseedView(spec debianConfigSpec) (preseedView, error) {
	v := preseedView{
		Locale:   spec.Locale,
		Keyboard: spec.Keyboard,
		Hostname: spec.Hostname,
		Domain:   spec.Domain,
		Timezone: spec.Timezone,
		Packages: strings.Join(spec.Packages, " "),
	}
	if n := spec.Network; n != nil {
		v.Iface = n.Interface
		if s := n.Static; s != nil {
			v.Static = true
			v.StaticAddress, v.StaticNetmask, v.StaticGateway = s.Address, s.Netmask, s.Gateway
			v.StaticNameservers = strings.Join(s.Nameservers, " ")
		}
	}
	if m := spec.Mirror; m != nil {
		// B2: d-i's choose-mirror ignores an operator hostname/directory unless
		// mirror/country is "manual" (official example-preseed). booty generates
		// these lines, so emitting the manual selector is booty's responsibility.
		v.MirrorManual = true
		v.MirrorHost, v.MirrorDir, v.MirrorProxy = m.Hostname, m.Directory, m.Proxy
	}
	if a := spec.Accounts; a != nil {
		v.HasAccounts = true
		v.RootHash = a.RootPasswordHash
		if u := a.User; u != nil {
			if u.Username == "" || u.PasswordHash == "" {
				return preseedView{}, errors.New("http: debianconfig: accounts.user requires username and password_hash")
			}
			v.HasUser = true
			v.Username = u.Username
			v.UserHash = u.PasswordHash
			v.UserFullname = cmp.Or(u.Fullname, u.Username)
		}
	}
	return v, nil
}

// preseedTemplateText emits the flat preseed. Layout convention: each debconf
// line lives INSIDE its {{ if }} block with its trailing newline BEFORE the
// {{ end }}, and blocks abut with no whitespace between {{ end }} and the next
// {{ if }} — so an unset field emits zero bytes (emit-only-what-is-set).
const preseedTemplateText = `{{ if .Locale }}d-i debian-installer/locale string {{ .Locale }}
{{ end }}{{ if .Keyboard }}d-i keyboard-configuration/xkb-keymap select {{ .Keyboard }}
{{ end }}{{ if .Iface }}d-i netcfg/choose_interface select {{ .Iface }}
{{ end }}{{ if .Static }}d-i netcfg/disable_autoconfig boolean true
d-i netcfg/get_ipaddress string {{ .StaticAddress }}
d-i netcfg/get_netmask string {{ .StaticNetmask }}
d-i netcfg/get_gateway string {{ .StaticGateway }}
{{ if .StaticNameservers }}d-i netcfg/get_nameservers string {{ .StaticNameservers }}
{{ end }}d-i netcfg/confirm_static boolean true
{{ end }}{{ if .Hostname }}d-i netcfg/get_hostname string {{ .Hostname }}
{{ end }}{{ if .Domain }}d-i netcfg/get_domain string {{ .Domain }}
{{ end }}{{ if .MirrorManual }}d-i mirror/country string manual
{{ end }}{{ if .MirrorHost }}d-i mirror/http/hostname string {{ .MirrorHost }}
{{ end }}{{ if .MirrorDir }}d-i mirror/http/directory string {{ .MirrorDir }}
{{ end }}{{ if .MirrorProxy }}d-i mirror/http/proxy string {{ .MirrorProxy }}
{{ end }}{{ if .Timezone }}d-i time/zone string {{ .Timezone }}
{{ end }}{{ if .HasAccounts }}{{ if .RootHash }}d-i passwd/root-login boolean true
d-i passwd/root-password-crypted password {{ .RootHash }}
{{ else }}d-i passwd/root-login boolean false
{{ end }}{{ if .HasUser }}d-i passwd/make-user boolean true
d-i passwd/user-fullname string {{ .UserFullname }}
d-i passwd/username string {{ .Username }}
d-i passwd/user-password-crypted password {{ .UserHash }}
{{ end }}{{ end }}{{ if .Packages }}d-i pkgsel/include string {{ .Packages }}
{{ end }}`

var preseedTmpl = template.Must(template.New("debianpreseed").Parse(preseedTemplateText))

// translateDebianConfig translates a rendered (post-template) debianconfig
// YAML source into a flat d-i preseed (design §5). booty owns this generation:
// no vendorable structured→preseed library exists. Coherence violations return
// errors the API surfaces as 422 via validateConfigSource's default arm.
func translateDebianConfig(rendered []byte) ([]byte, error) {
	var spec debianConfigSpec
	if err := yaml.Unmarshal(rendered, &spec); err != nil {
		return nil, fmt.Errorf("http: parse debianconfig: %w", err)
	}
	view, err := buildPreseedView(spec)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := preseedTmpl.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("http: emit preseed: %w", err)
	}
	return buf.Bytes(), nil
}
