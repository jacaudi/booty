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
	Disk     *debianDisk     `yaml:"disk"`
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

// debianDisk is the curated disk model (design §6): native partman primitives
// only. Defaults WITHIN a present disk block: layout=plain, filesystem=ext4,
// raid=none. No disk block at all -> no partman lines (I4).
type debianDisk struct {
	Devices    []string `yaml:"devices"`
	RAID       string   `yaml:"raid"`       // "" (=none) | none | mirror
	Layout     string   `yaml:"layout"`     // "" (=plain) | plain | lvm
	Filesystem string   `yaml:"filesystem"` // "" (=ext4) | ext4 | xfs
}

// diskView is the disk half of the emission view.
type diskView struct {
	Devices    string // space-joined
	Method     string // regular | lvm
	LVM        bool
	Filesystem string
}

// buildDiskView validates disk coherence (design §6.5 — only called when a
// disk block is present) and derives the disk emission view.
func buildDiskView(d *debianDisk) (*diskView, error) {
	layout := cmp.Or(d.Layout, "plain")
	fs := cmp.Or(d.Filesystem, "ext4")
	raid := cmp.Or(d.RAID, "none")
	if layout != "plain" && layout != "lvm" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.layout %q (want plain|lvm)", d.Layout)
	}
	if fs != "ext4" && fs != "xfs" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.filesystem %q (want ext4|xfs)", d.Filesystem)
	}
	if raid != "none" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.raid %q (want none)", d.RAID)
	}
	if len(d.Devices) == 0 {
		return nil, errors.New("http: debianconfig: disk.devices requires at least one device")
	}
	v := &diskView{Devices: strings.Join(d.Devices, " "), Filesystem: fs, LVM: layout == "lvm", Method: "regular"}
	if v.LVM {
		v.Method = "lvm"
	}
	return v, nil
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
	Disk                                        *diskView
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
	if spec.Disk != nil {
		dv, err := buildDiskView(spec.Disk)
		if err != nil {
			return preseedView{}, err
		}
		v.Disk = dv
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
{{ end }}{{ end }}{{ if .Disk }}d-i partman-auto/disk string {{ .Disk.Devices }}
d-i partman-auto/method string {{ .Disk.Method }}
{{ if .Disk.LVM }}d-i partman-lvm/device_remove_lvm boolean true
d-i partman-auto-lvm/guided_size string max
d-i partman-lvm/confirm boolean true
d-i partman-lvm/confirm_nooverwrite boolean true
{{ end }}d-i partman-auto/choose_recipe select atomic
d-i partman/default_filesystem string {{ .Disk.Filesystem }}
d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i grub-installer/bootdev string {{ .Disk.Devices }}
{{ end }}{{ if .Packages }}d-i pkgsel/include string {{ .Packages }}
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
