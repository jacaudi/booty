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
	Fullname          string   `yaml:"fullname"` // optional; falls back to username
	Username          string   `yaml:"username"`
	PasswordHash      string   `yaml:"password_hash"`
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys"` // lowered to late_command (M3)
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
	Mirror       bool
	DevCount     int
	BootParts    string // md /boot members, e.g. /dev/sda2#/dev/sdb2
	RootParts    string // md root members, e.g. /dev/sda3#/dev/sdb3
	BootDegraded bool
	ESPSyncCmd   string // ESP-sync late_command source (mirror only), composed into preseedView.LateCommand
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
	if raid != "none" && raid != "mirror" {
		return nil, fmt.Errorf("http: debianconfig: invalid disk.raid %q (want none|mirror)", d.RAID)
	}
	if raid == "mirror" && len(d.Devices) < 2 {
		return nil, errors.New("http: debianconfig: raid: mirror requires at least 2 disk.devices")
	}
	if len(d.Devices) == 0 {
		return nil, errors.New("http: debianconfig: disk.devices requires at least one device")
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
		// efibootmgr entry), so no MBR boot flag is needed.
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
// register a fallback UEFI boot entry, so the node still UEFI-boots if the
// primary disk dies. Grounded in std.rocks/gnulinux_mdadm_uefi.html (clone ESP
// + efibootmgr --create for the secondary disk). Runs inside the target during
// preseed/late_command; /boot/efi is the mounted primary ESP at that point.
// The efibootmgr loader path uses literal backslashes (\EFI\debian\shimx64.efi).
func espSyncLateCommand(devices []string) string {
	var cmds []string
	for i := 1; i < len(devices); i++ {
		dev := devices[i]
		esp := partitionName(dev, 1)
		label := fmt.Sprintf("debian (disk %d)", i+1)
		cmds = append(cmds,
			"in-target sh -c 'mkfs.vfat -F32 "+esp+"'",
			"in-target sh -c 'mount "+esp+" /mnt && cp -a /boot/efi/. /mnt/ && umount /mnt'",
			`in-target efibootmgr --create --disk `+dev+` --part 1 --label "`+label+`" --loader \EFI\debian\shimx64.efi`,
		)
	}
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
	var sshCmd string
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
			if len(u.SSHAuthorizedKeys) > 0 {
				for _, k := range u.SSHAuthorizedKeys {
					if err := validateSSHAuthorizedKey(k); err != nil {
						return preseedView{}, err
					}
				}
				sshCmd = sshLateCommand(u.Username, u.SSHAuthorizedKeys)
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
	// Compose the single d-i late_command line from ordered sources: ssh block
	// FIRST (source 1), then the ESP-sync (source 2, UEFI mirror). Task 7
	// appends the operator's late_command (source 3).
	var lateParts []string
	if sshCmd != "" {
		lateParts = append(lateParts, sshCmd)
	}
	if v.Disk != nil && v.Disk.ESPSyncCmd != "" {
		lateParts = append(lateParts, v.Disk.ESPSyncCmd)
	}
	v.LateCommand = strings.Join(lateParts, " ; ")
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
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
{{ else }}d-i partman-auto/choose_recipe select atomic
d-i partman/default_filesystem string {{ .Disk.Filesystem }}
{{ end }}d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i grub-installer/bootdev string {{ .Disk.Devices }}
{{ end }}{{ if .Packages }}d-i pkgsel/include string {{ .Packages }}
{{ end }}{{ if .LateCommand }}d-i preseed/late_command string {{ .LateCommand }}
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
