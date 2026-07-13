package http

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

	LateCommand stringOrList `yaml:"late_command"` // block scalar OR a YAML list (D3)
	RawPreseed  string       `yaml:"raw_preseed"`  // verbatim preseed lines, appended LAST
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
	Fullname          string   `yaml:"fullname"` // optional; falls back to username
	Username          string   `yaml:"username"`
	PasswordHash      string   `yaml:"password_hash"`
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys"` // lowered to late_command (M3)
	Sudo              sudoMode `yaml:"sudo"`                // none | password | nopasswd (D2)
}

// sudoMode is the per-user sudo authorization level (design D2). The zero value
// sudoNone means "no sudo" so an ABSENT sudo: field (UnmarshalYAML never called)
// is correctly none. nopasswd -> passwordless sudo via a NOPASSWD drop-in;
// password -> sudo group only (interactive, prompts for the user's password).
type sudoMode int

const (
	sudoNone sudoMode = iota
	sudoPassword
	sudoNopasswd
)

// UnmarshalYAML closes the sudo: input matrix (F3): a YAML string
// (nopasswd|password) or bool (false->none, true->nopasswd, a friendly alias),
// plus null/absent -> none; everything else (empty string, number, sequence,
// mapping) is a validation error surfaced as 422 upstream.
func (m *sudoMode) UnmarshalYAML(node *yaml.Node) error {
	switch node.Tag {
	case "!!null":
		*m = sudoNone
		return nil
	case "!!bool":
		var b bool
		if err := node.Decode(&b); err != nil {
			return err
		}
		if b {
			*m = sudoNopasswd
		} else {
			*m = sudoNone
		}
		return nil
	case "!!str":
		switch node.Value {
		case "nopasswd":
			*m = sudoNopasswd
		case "password":
			*m = sudoPassword
		default:
			return fmt.Errorf("http: debianconfig: invalid accounts.user.sudo %q (want nopasswd|password|false|true)", node.Value)
		}
		return nil
	default:
		return errors.New("http: debianconfig: accounts.user.sudo must be a string (nopasswd|password) or a bool")
	}
}

// debianDisk is the curated disk model (design §6): native partman primitives
// only. Defaults WITHIN a present disk block: layout=plain, filesystem=ext4,
// raid=none. No disk block at all -> no partman lines (I4).
type debianDisk struct {
	Devices      []string `yaml:"devices"`
	RAID         string   `yaml:"raid"`          // "" (=none) | none | mirror
	Layout       string   `yaml:"layout"`        // "" (=plain) | plain | lvm
	Filesystem   string   `yaml:"filesystem"`    // "" (=ext4) | ext4 | xfs
	BootDegraded *bool    `yaml:"boot_degraded"` // mirror only; default true
	ExpertRecipe string   `yaml:"expert_recipe"` // raw partman override; replaces the curated recipe
}

// diskView is the disk half of the emission view. UEFI mirror recipes are
// device-list-parameterized: BootParts/RootParts enumerate partition 2/3 of
// every member device (partition 1 is the per-disk ESP), '#'-joined per
// partman-auto-raid/recipe syntax.
type diskView struct {
	Devices    string // space-joined
	Method     string // regular | lvm | raid
	LVM        bool
	Filesystem string
	// mirror only:
	Mirror          bool
	DevCount        int
	BootParts       string // md /boot members, e.g. /dev/sda2#/dev/sdb2
	RootParts       string // md root members, e.g. /dev/sda3#/dev/sdb3
	BootDegraded    bool
	ESPSyncCmd      string // ESP-sync late_command source (mirror only), composed into preseedView.LateCommand
	ExpertRecipe    string // raw partman override (curated knobs bypassed)
	BootDegradedSet bool   // expert path: emit boot_degraded only when the operator set it
}

// errDiskNeedsDevice is returned by buildDiskView when disk.devices is empty:
// every disk path (expert and curated alike) requires at least one device.
var errDiskNeedsDevice = errors.New("http: debianconfig: disk.devices requires at least one device")

// buildDiskView validates disk coherence (design §6.5 — only called when a
// disk block is present) and derives the disk emission view.
func buildDiskView(d *debianDisk) (*diskView, error) {
	if len(d.Devices) == 0 {
		return nil, errDiskNeedsDevice
	}
	if d.ExpertRecipe != "" {
		// B1: a multi-line partman-auto/expert_recipe debconf value needs a
		// trailing `\` on every non-final physical line — which an operator's
		// YAML block scalar lacks, so debconf would parse line 2 as a bogus
		// directive and drop the whole recipe. Flatten to ONE physical line:
		// partman recipes are whitespace-delimited, so collapsing all runs of
		// whitespace to single spaces is lossless (the ` . ` entry separators
		// survive as tokens).
		v := &diskView{Devices: strings.Join(d.Devices, " "), ExpertRecipe: strings.Join(strings.Fields(d.ExpertRecipe), " ")}
		if d.BootDegraded != nil {
			v.BootDegradedSet, v.BootDegraded = true, *d.BootDegraded
		}
		return v, nil
	}
	layout := cmp.Or(d.Layout, "plain")
	fs := cmp.Or(d.Filesystem, "ext4")
	raid := cmp.Or(d.RAID, "none")
	if layout != "plain" && layout != "lvm" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.layout %q (want plain|lvm)", d.Layout)
	}
	if fs != "ext4" && fs != "xfs" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.filesystem %q (want ext4|xfs)", d.Filesystem)
	}
	if raid != "none" && raid != "mirror" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.raid %q (want none|mirror)", d.RAID)
	}
	if raid == "mirror" && len(d.Devices) < 2 {
		return nil, errors.New("http: debianconfig: raid: mirror requires at least 2 disk.devices")
	}
	v := &diskView{Devices: strings.Join(d.Devices, " "), Filesystem: fs, LVM: layout == "lvm", Method: "regular"}
	if v.LVM {
		v.Method = "lvm"
	}
	if raid == "mirror" {
		// UEFI-native curated mirror (target nodes are UEFI). Layout per member
		// disk: partition 1 = a per-disk ESP (method{ efi }, ~512M, NOT a raid
		// member); partition 2 = md /boot (ext4); partition 3 = md root. The ESP
		// CANNOT live on mdadm: firmware writes to /boot/efi directly before md
		// assembles, so a mirrored ESP desyncs and d-i refuses /boot/efi on md —
		// hence one plain ESP per disk, cloned post-install by the ESP-sync
		// late_command (v.ESPSyncCmd, composed below). Recipe structure grounded in the bearice
		// EFI+RAID1 gist (github.com/.../331a954d86d890d9dbeacdd7de3aabe8) and
		// std.rocks/gnulinux_mdadm_uefi.html — adapt, do not reinvent.
		// M1: assumes partman numbers partitions in recipe order (/dev/sdaN <->
		// the Nth expert_recipe entry) — documented partman behavior, not
		// strictly guaranteed.
		// M3: $bootable{ } is carried on the per-disk ESP entry (per the
		// reference) and intentionally OMITTED on the md /boot/root raid members;
		// grub-efi installs to every member's ESP (grub-installer + the ESP-sync
		// clone below, plus the removable-media fallback entry force-efi-extra-
		// removable writes onto each), so no MBR boot flag is needed.
		v.Mirror = true
		v.Method = "raid"
		v.DevCount = len(d.Devices)
		v.BootParts = memberParts(d.Devices, 2) // partition 1 is the ESP
		v.RootParts = memberParts(d.Devices, 3)
		v.BootDegraded = true // unattended-node default (design §6.2)
		if d.BootDegraded != nil {
			v.BootDegraded = *d.BootDegraded
		}
		v.ESPSyncCmd = espSyncLateCommand(d.Devices)
	}
	return v, nil
}

// espSyncLateCommand builds the redundancy-critical ESP-sync fragment: the ESP
// CANNOT be mirrored (§ buildDiskView comment), so after install we clone the
// primary disk's ESP (device 0, partition 1) onto every OTHER member's ESP and
// make /boot/efi's fstab entry nofail, so the node still boots if the primary
// disk dies. The actual fallback boot path is the UEFI removable-media entry
// (\EFI\boot\bootx64.efi) that grub-installer writes onto every ESP when d-i
// grub-installer/force-efi-extra-removable is set (see the mirror branch of
// preseedTemplateText) — NOT an efibootmgr/NVRAM boot entry: lab-validated,
// `in-target efibootmgr` fails at late_command time (no EFI variable access
// inside the d-i chroot) and efibootmgr isn't installed outside it either, so
// an NVRAM entry can never actually be created here. Runs inside the target
// during preseed/late_command; /boot/efi is the mounted primary ESP at that
// point, and /etc/fstab (already written by d-i) still references the PRIMARY
// ESP by UUID — the fstab edit below makes that mount nofail so degraded boot
// (primary disk dead) doesn't time out into emergency mode.
func espSyncLateCommand(devices []string) string {
	var cmds []string
	for i := 1; i < len(devices); i++ {
		esp := partitionName(devices[i], 1)
		cmds = append(cmds,
			"in-target sh -c 'mkfs.vfat -F32 "+esp+"'",
			"in-target sh -c 'mount "+esp+" /mnt && cp -a /boot/efi/. /mnt/ && umount /mnt'",
		)
	}
	// Make /boot/efi nofail: fstab references the PRIMARY ESP by UUID, so if that disk dies,
	// degraded boot must not time out and drop to emergency. (The removable-media fallback,
	// enabled via grub-installer/force-efi-extra-removable and cloned onto every ESP above,
	// is what actually boots a surviving disk — no efibootmgr/NVRAM entry is needed.)
	cmds = append(cmds, `in-target sed -i -E '\| /boot/efi |s|(vfat[[:space:]]+)([^[:space:]]+)|\1\2,nofail,x-systemd.device-timeout=1|' /etc/fstab`)
	return strings.Join(cmds, " ; ")
}

// sshKeyShellMetacharacters are the characters that are live shell syntax in
// the context sshLateCommand interpolates each key into: a double-quoted
// printf argument NESTED inside a single-quoted `sh -c '...'` string. Inside
// that double-quoted context, '"' closes the argument early, '$' and '`'
// trigger expansion/substitution, and '\' can shift quoting — any one of
// these turns a "key" into injected shell syntax. A leading single quote
// would additionally break out of the outer sh -c string.
const sshKeyShellMetacharacters = `'"$` + "`" + `\`

// validateSSHAuthorizedKey rejects a key that cannot be safely interpolated
// into sshLateCommand's double-quoted-inside-single-quoted shell context, and
// rejects an empty/whitespace-only key (which would printf a blank line into
// authorized_keys). A real `type base64 [comment]` authorized_keys line never
// legitimately contains any of these characters.
func validateSSHAuthorizedKey(k string) error {
	if strings.TrimSpace(k) == "" {
		return errors.New("http: debianconfig: ssh_authorized_keys entries must not be empty")
	}
	if strings.ContainsAny(k, sshKeyShellMetacharacters) {
		return errors.New("http: debianconfig: ssh_authorized_keys must not contain shell metacharacters ('\"$`\\)")
	}
	for _, r := range k {
		if r < 0x20 {
			return errors.New("http: debianconfig: ssh_authorized_keys must not contain control characters")
		}
	}
	return nil
}

// usernameRE mirrors Debian useradd's default NAME_REGEX shape and doubles as
// the shell-injection guardrail: username is interpolated by raw string
// concatenation into a ROOT shell context (sshLateCommand's /home/<username>
// and chown -R <username>:<username>) and into the preseed value line (d-i
// passwd/username string <username>) — the same sink ssh_authorized_keys is
// hardened against via validateSSHAuthorizedKey. The leading [a-z_] and the
// restricted character class exclude every shell metacharacter, whitespace,
// quote, and newline; length is capped at 32 (Debian useradd's limit).
var usernameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// validateUsername rejects a username that cannot be safely interpolated into
// sshLateCommand's root shell context or the preseed value line, and rejects
// an empty username with a clear message (the pattern already rejects empty,
// this just names the reason).
func validateUsername(u string) error {
	if u == "" {
		return errors.New("http: debianconfig: accounts.user requires a username")
	}
	if len(u) > 32 || !usernameRE.MatchString(u) {
		return fmt.Errorf("http: debianconfig: invalid accounts.user.username %q (must match %s, max 32 chars)", u, usernameRE.String())
	}
	return nil
}

// sshLateCommand lowers ssh_authorized_keys to an in-target late_command
// fragment (M3): d-i has no native preseed directive for authorized keys.
// The printf "%s\n" runs at install time inside the target; each key is a
// double-quoted argument inside the single-quoted sh -c string (keys are
// validated by validateSSHAuthorizedKey to contain no shell metacharacters).
func sshLateCommand(username string, keys []string) string {
	home := "/home/" + username
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = `"` + k + `"`
	}
	return "in-target mkdir -p " + home + "/.ssh ; " +
		`in-target sh -c 'printf "%s\n" ` + strings.Join(quoted, " ") + " >> " + home + "/.ssh/authorized_keys' ; " +
		"in-target chown -R " + username + ":" + username + " " + home + "/.ssh ; " +
		"in-target chmod 700 " + home + "/.ssh ; " +
		"in-target chmod 600 " + home + "/.ssh/authorized_keys"
}

// sudoLateCommand builds the sudo-setup late_command fragment (design D2). Both
// modes add the user to the sudo group (path-uniform, F7). nopasswd ALSO writes
// a 440 /etc/sudoers.d/<user> NOPASSWD drop-in via POSIX printf (NOT a bashism —
// the '<<<' here-string fails under d-i's sh) then chmod 440. <username> is the
// already-validateUsername-validated name (^[a-z_][a-z0-9_-]*$), so it is
// injection-safe in the shell context AND is never a filename sudo ignores
// (the charset excludes '.' and '~').
func sudoLateCommand(mode sudoMode, username string) string {
	group := "in-target usermod -aG sudo " + username
	if mode != sudoNopasswd {
		return group
	}
	drop := "/etc/sudoers.d/" + username
	return group + " ; " +
		`in-target sh -c 'printf "%s\n" "` + username + ` ALL=(ALL) NOPASSWD:ALL" > ` + drop + `' ; ` +
		"in-target chmod 440 " + drop
}

// appendIfAbsent appends name to pkgs only when want is true and name is not
// already present (D4/D2 auto-add + dedup, F1). The operator's list order is
// preserved; auto-adds land at the end.
func appendIfAbsent(pkgs []string, name string, want bool) []string {
	if !want || slices.Contains(pkgs, name) {
		return pkgs
	}
	return append(pkgs, name)
}

// stringOrList lets late_command be authored as either a YAML block scalar (the
// original form) or a YAML sequence of commands (D3). A sequence joins with
// "\n" so it feeds flattenLateCommand identically to a multi-line block —
// flattenLateCommand stays the single ';'-joining normalizer (DRY). null/absent
// -> "" (back-compat: an absent late_command emits no line).
type stringOrList string

func (s *stringOrList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var str string
		if err := node.Decode(&str); err != nil { // null decodes to ""
			return err
		}
		*s = stringOrList(str)
		return nil
	case yaml.SequenceNode:
		var items []string
		if err := node.Decode(&items); err != nil {
			return err
		}
		*s = stringOrList(strings.Join(items, "\n"))
		return nil
	default:
		return errors.New("http: debianconfig: late_command must be a string or a list of strings")
	}
}

// flattenLateCommand flattens a (possibly multiline) operator late_command to
// the single debconf line d-i expects: lines trimmed, empties dropped,
// "; "-joined. Commands must therefore be independently sequenceable.
func flattenLateCommand(raw string) string {
	var lines []string
	for line := range strings.SplitSeq(raw, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " ; ")
}

// partitionName returns device's nth partition node, inserting the "p"
// separator udev uses when the device name ends in a digit
// (/dev/nvme0n1 -> /dev/nvme0n1p1); classic /dev/sdX concatenates.
func partitionName(device string, n int) string {
	if len(device) > 0 && device[len(device)-1] >= '0' && device[len(device)-1] <= '9' {
		return fmt.Sprintf("%sp%d", device, n)
	}
	return fmt.Sprintf("%s%d", device, n)
}

// memberParts enumerates partition n of every member device, '#'-joined per
// partman-auto-raid/recipe syntax.
func memberParts(devices []string, n int) string {
	parts := make([]string, len(devices))
	for i, d := range devices {
		parts[i] = partitionName(d, n)
	}
	return strings.Join(parts, "#")
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
	LateCommand                                 string
	RawPreseed                                  string
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
	}
	var sshCmd, sudoCmd string
	var needOpenSSH, needSudo bool
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
			// F5 validation order: (1) username, (2) reachability (D1),
			// (3) sudo coherence.
			if err := validateUsername(u.Username); err != nil {
				return preseedView{}, err
			}
			hasKeys := len(u.SSHAuthorizedKeys) > 0
			if u.PasswordHash == "" && !hasKeys {
				return preseedView{}, errors.New("http: debianconfig: accounts.user requires password_hash or ssh_authorized_keys")
			}
			if u.Sudo == sudoPassword && u.PasswordHash == "" {
				return preseedView{}, errors.New("http: debianconfig: sudo: password requires accounts.user.password_hash")
			}
			v.HasUser = true
			v.Username = u.Username
			v.UserHash = cmp.Or(u.PasswordHash, "*") // D1: omitted -> locked sentinel
			v.UserFullname = cmp.Or(u.Fullname, u.Username)
			if hasKeys {
				for _, k := range u.SSHAuthorizedKeys {
					if err := validateSSHAuthorizedKey(k); err != nil {
						return preseedView{}, err
					}
				}
				sshCmd = sshLateCommand(u.Username, u.SSHAuthorizedKeys)
				needOpenSSH = true
			}
			if u.Sudo != sudoNone {
				sudoCmd = sudoLateCommand(u.Sudo, u.Username)
				needSudo = true
			}
		}
	}
	if spec.Disk != nil {
		dv, err := buildDiskView(spec.Disk)
		if err != nil {
			return preseedView{}, err
		}
		v.Disk = dv
	}
	// Compose ONE late_command from ordered sources (design §4, M3): the ssh
	// fragment FIRST, then the ESP-sync (UEFI mirror), then the operator's own
	// commands — all run, ordering/ownership deterministic.
	var lateParts []string
	if sshCmd != "" {
		lateParts = append(lateParts, sshCmd)
	}
	if sudoCmd != "" {
		lateParts = append(lateParts, sudoCmd)
	}
	if v.Disk != nil && v.Disk.ESPSyncCmd != "" {
		lateParts = append(lateParts, v.Disk.ESPSyncCmd)
	}
	if op := flattenLateCommand(string(spec.LateCommand)); op != "" {
		lateParts = append(lateParts, op)
	}
	v.LateCommand = strings.Join(lateParts, " ; ")
	// raw_preseed is appended LAST (design §4): later duplicate debconf answers
	// win, so the hatch can always override a curated line.
	v.RawPreseed = strings.TrimRight(spec.RawPreseed, "\n")
	pkgs := slices.Clone(spec.Packages)
	pkgs = appendIfAbsent(pkgs, "openssh-server", needOpenSSH) // F1: openssh-server first
	pkgs = appendIfAbsent(pkgs, "sudo", needSudo)              // then sudo
	v.Packages = strings.Join(pkgs, " ")
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
{{ if .Disk.ExpertRecipe }}d-i partman-auto/method string regular
d-i partman-auto/expert_recipe string {{ .Disk.ExpertRecipe }}
{{ if .Disk.BootDegradedSet }}d-i mdadm/boot_degraded boolean {{ .Disk.BootDegraded }}
{{ end }}{{ else }}d-i partman-auto/method string {{ .Disk.Method }}
{{ if .Disk.Mirror }}d-i partman-efi/non_efi_system boolean true
d-i partman-md/device_remove_md boolean true
{{ end }}{{ if .Disk.LVM }}d-i partman-lvm/device_remove_lvm boolean true
d-i partman-auto-lvm/guided_size string max
d-i partman-lvm/confirm boolean true
d-i partman-lvm/confirm_nooverwrite boolean true
{{ end }}{{ if .Disk.Mirror }}d-i partman-auto/expert_recipe string \
    multiraid :: \
    512 512 640 free $bootable{ } method{ efi } format{ } . \
    512 512 512 raid $primary{ } method{ raid } . \
    1000 10000 -1 raid $primary{ } method{ raid } .{{ if .Disk.LVM }} \
    2048 4096 -1 {{ .Disk.Filesystem }} $defaultignore{ } $lvmok{ } method{ format } format{ } use_filesystem{ } filesystem{ {{ .Disk.Filesystem }} } mountpoint{ / } .{{ end }}
d-i partman-auto-raid/recipe string \
    1 {{ .Disk.DevCount }} 0 ext4 /boot {{ .Disk.BootParts }} . \
    1 {{ .Disk.DevCount }} 0 {{ if .Disk.LVM }}lvm -{{ else }}{{ .Disk.Filesystem }} /{{ end }} {{ .Disk.RootParts }} .
d-i mdadm/boot_degraded boolean {{ .Disk.BootDegraded }}
d-i grub-installer/force-efi-extra-removable boolean true
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
{{ else }}d-i partman-auto/choose_recipe select atomic
d-i partman/default_filesystem string {{ .Disk.Filesystem }}
{{ end }}{{ end }}d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i partman-basicfilesystems/no_swap boolean false
d-i grub-installer/bootdev string {{ .Disk.Devices }}
{{ end }}{{ if .Packages }}d-i pkgsel/include string {{ .Packages }}
{{ end }}{{ if .LateCommand }}d-i preseed/late_command string {{ .LateCommand }}
{{ end }}{{ if .RawPreseed }}{{ .RawPreseed }}
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
