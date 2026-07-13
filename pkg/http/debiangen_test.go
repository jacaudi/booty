package http

import (
	"fmt"
	"strings"
	"testing"
)

// translate is a test convenience: translateDebianConfig over a YAML source.
func translate(t *testing.T, src string) string {
	t.Helper()
	out, err := translateDebianConfig([]byte(src))
	if err != nil {
		t.Fatalf("translateDebianConfig: %v", err)
	}
	return string(out)
}

// TestTranslateDebianConfigMinimal: emit-only-what-is-set — a hostname-only
// spec emits exactly one line, no partman lines, no fabricated defaults.
func TestTranslateDebianConfigMinimal(t *testing.T) {
	got := translate(t, "hostname: node1\n")
	want := "d-i netcfg/get_hostname string node1\n"
	if got != want {
		t.Errorf("minimal spec:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestTranslateDebianConfigEmptyIsEmpty: a fully-unset spec emits nothing.
func TestTranslateDebianConfigEmptyIsEmpty(t *testing.T) {
	if got := translate(t, ""); got != "" {
		t.Errorf("empty spec emitted %q, want empty", got)
	}
}

// TestTranslateDebianConfigFull pins the full non-disk curated surface
// byte-for-byte, including emission order (design §4).
func TestTranslateDebianConfigFull(t *testing.T) {
	src := `hostname: node1
domain: cluster.local
locale: en_US.UTF-8
timezone: Etc/UTC
keyboard: us
mirror:
  hostname: deb.debian.org
  directory: /debian
network:
  interface: auto
  static:
    address: 10.0.0.10
    netmask: 255.255.255.0
    gateway: 10.0.0.1
    nameservers: [10.0.0.1, 10.0.0.2]
accounts:
  root_password_hash: $6$roothash
  user:
    fullname: Ops
    username: ops
    password_hash: $6$userhash
packages:
  - openssh-server
  - qemu-guest-agent
`
	want := `d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/disable_autoconfig boolean true
d-i netcfg/get_ipaddress string 10.0.0.10
d-i netcfg/get_netmask string 255.255.255.0
d-i netcfg/get_gateway string 10.0.0.1
d-i netcfg/get_nameservers string 10.0.0.1 10.0.0.2
d-i netcfg/confirm_static boolean true
d-i netcfg/get_hostname string node1
d-i netcfg/get_domain string cluster.local
d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i time/zone string Etc/UTC
d-i passwd/root-login boolean true
d-i passwd/root-password-crypted password $6$roothash
d-i passwd/make-user boolean true
d-i passwd/user-fullname string Ops
d-i passwd/username string ops
d-i passwd/user-password-crypted password $6$userhash
d-i pkgsel/include string openssh-server qemu-guest-agent
`
	if got := translate(t, src); got != want {
		t.Errorf("full spec:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTranslateDebianConfigRootOmittedDisablesRootLogin: within a PRESENT
// accounts block, an omitted root_password_hash disables root login — the
// design-pinned safe default (§4/§10). Fullname falls back to username.
func TestTranslateDebianConfigRootOmittedDisablesRootLogin(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n"
	want := `d-i passwd/root-login boolean false
d-i passwd/make-user boolean true
d-i passwd/user-fullname string ops
d-i passwd/username string ops
d-i passwd/user-password-crypted password $6$h
`
	if got := translate(t, src); got != want {
		t.Errorf("root-omitted:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTranslateDebianConfigUserRequiresUsernameAndHash: coherence — a present
// user block without username or password_hash is rejected (422 upstream).
func TestTranslateDebianConfigUserRequiresUsernameAndHash(t *testing.T) {
	if _, err := translateDebianConfig([]byte("accounts:\n  user:\n    username: ops\n")); err == nil {
		t.Error("user without password_hash must be rejected")
	}
	if _, err := translateDebianConfig([]byte("accounts:\n  user:\n    password_hash: $6$h\n")); err == nil {
		t.Error("user without username must be rejected")
	}
}

// TestTranslateDebianConfigUsernameShellInjectionRejected: username is
// interpolated by raw string concatenation into a ROOT shell context
// (sshLateCommand's /home/<username> and chown -R <username>:<username>) and
// into the preseed value line (d-i passwd/username string <username>) — the
// SAME shell context ssh_authorized_keys is hardened against. A username
// carrying shell metacharacters, whitespace, or control characters must be
// rejected at generation time (422 upstream), not reach either sink.
func TestTranslateDebianConfigUsernameShellInjectionRejected(t *testing.T) {
	bad := []string{
		"x; touch /tmp/pwned",
		"a$(id)",
		"has space",
		`has"quote`,
		"has`tick",
		"has\nnewline",
	}
	for _, u := range bad {
		src := "accounts:\n  user:\n    username: " + fmt.Sprintf("%q", u) + "\n    password_hash: $6$h\n"
		if _, err := translateDebianConfig([]byte(src)); err == nil {
			t.Errorf("username %q must be rejected", u)
		}
	}
}

// TestTranslateDebianConfigUsernameNormalAccepted: a normal lowercase
// username (the shape every golden in this file already uses) is accepted —
// pins that validateUsername does not over-reject.
func TestTranslateDebianConfigUsernameNormalAccepted(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n"
	if _, err := translateDebianConfig([]byte(src)); err != nil {
		t.Errorf("normal username rejected: %v", err)
	}
}

// TestTranslateDebianConfigBadYAMLIsError: unparseable YAML is an error, not
// a silent empty preseed.
func TestTranslateDebianConfigBadYAMLIsError(t *testing.T) {
	if _, err := translateDebianConfig([]byte(":\n  - not yaml: [")); err == nil {
		t.Error("bad YAML must be rejected")
	}
	if _, err := translateDebianConfig([]byte("hostname: node1")); err != nil {
		t.Errorf("valid YAML rejected: %v", err)
	}
}

// partmanTail is the confirm tail every curated disk recipe ends with (before
// the grub bootdev line). Test-side const — golden wants stay literal text.
const partmanTail = `d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i partman-basicfilesystems/no_swap boolean false
`

const lvmBlock = `d-i partman-lvm/device_remove_lvm boolean true
d-i partman-auto-lvm/guided_size string max
d-i partman-lvm/confirm boolean true
d-i partman-lvm/confirm_nooverwrite boolean true
`

// TestTranslateDebianConfigSingleDisk golden-pins the four raid:none combos
// (design §6.2 matrix, non-mirror half). Defaults inside a present disk block:
// layout=plain, filesystem=ext4, raid=none.
func TestTranslateDebianConfigSingleDisk(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "plain-ext4-defaults",
			src:  "disk:\n  devices: [/dev/sda]\n",
			want: "d-i partman-auto/disk string /dev/sda\n" +
				"d-i partman-auto/method string regular\n" +
				"d-i partman-auto/choose_recipe select atomic\n" +
				"d-i partman/default_filesystem string ext4\n" +
				partmanTail +
				"d-i grub-installer/bootdev string /dev/sda\n",
		},
		{
			name: "plain-xfs",
			src:  "disk:\n  devices: [/dev/sda]\n  layout: plain\n  filesystem: xfs\n  raid: none\n",
			want: "d-i partman-auto/disk string /dev/sda\n" +
				"d-i partman-auto/method string regular\n" +
				"d-i partman-auto/choose_recipe select atomic\n" +
				"d-i partman/default_filesystem string xfs\n" +
				partmanTail +
				"d-i grub-installer/bootdev string /dev/sda\n",
		},
		{
			name: "lvm-ext4",
			src:  "disk:\n  devices: [/dev/sda]\n  layout: lvm\n  filesystem: ext4\n",
			want: "d-i partman-auto/disk string /dev/sda\n" +
				"d-i partman-auto/method string lvm\n" +
				lvmBlock +
				"d-i partman-auto/choose_recipe select atomic\n" +
				"d-i partman/default_filesystem string ext4\n" +
				partmanTail +
				"d-i grub-installer/bootdev string /dev/sda\n",
		},
		{
			name: "lvm-xfs",
			src:  "disk:\n  devices: [/dev/sda]\n  layout: lvm\n  filesystem: xfs\n",
			want: "d-i partman-auto/disk string /dev/sda\n" +
				"d-i partman-auto/method string lvm\n" +
				lvmBlock +
				"d-i partman-auto/choose_recipe select atomic\n" +
				"d-i partman/default_filesystem string xfs\n" +
				partmanTail +
				"d-i grub-installer/bootdev string /dev/sda\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := translate(t, c.src); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

// TestTranslateDebianConfigDiskCoherence: checks fire ONLY when a disk block
// is present (I4/design §6.5) — no disk block at all is valid and emits no
// partman lines (already pinned by TestTranslateDebianConfigMinimal).
func TestTranslateDebianConfigDiskCoherence(t *testing.T) {
	reject := []struct{ name, src string }{
		{"no-devices", "disk:\n  layout: plain\n"},
		{"bad-layout", "disk:\n  devices: [/dev/sda]\n  layout: zfsish\n"},
		{"bad-filesystem", "disk:\n  devices: [/dev/sda]\n  filesystem: btrfs\n"},
		{"bad-raid", "disk:\n  devices: [/dev/sda]\n  raid: raid5\n"},
	}
	for _, c := range reject {
		t.Run(c.name, func(t *testing.T) {
			if _, err := translateDebianConfig([]byte(c.src)); err == nil {
				t.Errorf("%s must be rejected", c.name)
			}
		})
	}
}

// espSyncSdaSdb is the ESP-sync late_command line emitted for a 2-disk
// /dev/sda + /dev/sdb UEFI mirror: mkfs the secondary ESP, clone the primary
// ESP onto it, and make /boot/efi's fstab entry nofail so the node still boots
// if the primary disk dies (Task 5 composes this as the sole late_command
// source; Tasks 6/7 prepend ssh / append operator). No efibootmgr/NVRAM entry:
// lab-validated, `in-target efibootmgr` fails at late_command time (no EFI
// variable access inside the d-i chroot); the removable-media fallback (see
// force-efi-extra-removable in the mirror preseed) is what boots a surviving
// disk instead. Trailing newline: it is the last emitted line for these
// accounts-less specs.
const espSyncSdaSdb = `d-i preseed/late_command string in-target sh -c 'mkfs.vfat -F32 /dev/sdb1' ; in-target sh -c 'mount /dev/sdb1 /mnt && cp -a /boot/efi/. /mnt/ && umount /mnt' ; in-target sed -i -E '\| /boot/efi |s|(vfat[[:space:]]+)([^[:space:]]+)|\1\2,nofail,x-systemd.device-timeout=1|' /etc/fstab
`

// TestTranslateDebianConfigMirror golden-pins the four raid:mirror combos —
// UEFI-native (target nodes are UEFI). Each member disk gets its OWN small ESP
// (~512M, method{ efi }, NOT a raid member — the ESP cannot be mirrored: the
// firmware writes to it directly before mdadm assembles, so a mirrored ESP
// desyncs and d-i refuses /boot/efi on md). The remainder of each disk is a
// raid member: md /boot (ext4, partition 2) + md root (partition 3). Recipe
// structure grounded in the bearice EFI+RAID1 gist and std.rocks mdadm-uefi
// (cited in debiangen.go). The per-disk ESP entry, partman-efi, the
// device-enumerated partman-auto-raid/recipe, mdadm/boot_degraded, and the
// LVM-on-md nesting are the POINT of these tests, not incidental.
//
// PROVISIONAL: these pin BYTES, not install-correctness — see the netboot-lab
// UEFI gate below. The redundancy-critical ESP-sync late_command IS emitted
// here (it is intrinsic to a UEFI mirror), pinned as espSyncSdaSdb. It is the
// sole late_command source for these accounts-less specs; Task 6 prepends the
// ssh block and Task 7 appends the operator's own late_command, giving the
// final 3-source order: ssh -> ESP-sync -> operator.
func TestTranslateDebianConfigMirror(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "plain-ext4-mirror",
			src:  "disk:\n  devices: [/dev/sda, /dev/sdb]\n  raid: mirror\n",
			want: `d-i partman-auto/disk string /dev/sda /dev/sdb
d-i partman-auto/method string raid
d-i partman-efi/non_efi_system boolean true
d-i partman-md/device_remove_md boolean true
d-i partman-auto/expert_recipe string \
    multiraid :: \
    512 512 640 free $bootable{ } method{ efi } format{ } . \
    512 512 512 raid $primary{ } method{ raid } . \
    1000 10000 -1 raid $primary{ } method{ raid } .
d-i partman-auto-raid/recipe string \
    1 2 0 ext4 /boot /dev/sda2#/dev/sdb2 . \
    1 2 0 ext4 / /dev/sda3#/dev/sdb3 .
d-i mdadm/boot_degraded boolean true
d-i grub-installer/force-efi-extra-removable boolean true
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
` + partmanTail + "d-i grub-installer/bootdev string /dev/sda /dev/sdb\n" + espSyncSdaSdb,
		},
		{
			name: "plain-xfs-mirror",
			src:  "disk:\n  devices: [/dev/sda, /dev/sdb]\n  raid: mirror\n  filesystem: xfs\n",
			want: `d-i partman-auto/disk string /dev/sda /dev/sdb
d-i partman-auto/method string raid
d-i partman-efi/non_efi_system boolean true
d-i partman-md/device_remove_md boolean true
d-i partman-auto/expert_recipe string \
    multiraid :: \
    512 512 640 free $bootable{ } method{ efi } format{ } . \
    512 512 512 raid $primary{ } method{ raid } . \
    1000 10000 -1 raid $primary{ } method{ raid } .
d-i partman-auto-raid/recipe string \
    1 2 0 ext4 /boot /dev/sda2#/dev/sdb2 . \
    1 2 0 xfs / /dev/sda3#/dev/sdb3 .
d-i mdadm/boot_degraded boolean true
d-i grub-installer/force-efi-extra-removable boolean true
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
` + partmanTail + "d-i grub-installer/bootdev string /dev/sda /dev/sdb\n" + espSyncSdaSdb,
		},
		{
			name: "lvm-ext4-mirror",
			src:  "disk:\n  devices: [/dev/sda, /dev/sdb]\n  raid: mirror\n  layout: lvm\n",
			want: `d-i partman-auto/disk string /dev/sda /dev/sdb
d-i partman-auto/method string raid
d-i partman-efi/non_efi_system boolean true
d-i partman-md/device_remove_md boolean true
` + lvmBlock + `d-i partman-auto/expert_recipe string \
    multiraid :: \
    512 512 640 free $bootable{ } method{ efi } format{ } . \
    512 512 512 raid $primary{ } method{ raid } . \
    1000 10000 -1 raid $primary{ } method{ raid } . \
    2048 4096 -1 ext4 $defaultignore{ } $lvmok{ } method{ format } format{ } use_filesystem{ } filesystem{ ext4 } mountpoint{ / } .
d-i partman-auto-raid/recipe string \
    1 2 0 ext4 /boot /dev/sda2#/dev/sdb2 . \
    1 2 0 lvm - /dev/sda3#/dev/sdb3 .
d-i mdadm/boot_degraded boolean true
d-i grub-installer/force-efi-extra-removable boolean true
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
` + partmanTail + "d-i grub-installer/bootdev string /dev/sda /dev/sdb\n" + espSyncSdaSdb,
		},
		{
			name: "lvm-xfs-mirror",
			src:  "disk:\n  devices: [/dev/sda, /dev/sdb]\n  raid: mirror\n  layout: lvm\n  filesystem: xfs\n",
			want: `d-i partman-auto/disk string /dev/sda /dev/sdb
d-i partman-auto/method string raid
d-i partman-efi/non_efi_system boolean true
d-i partman-md/device_remove_md boolean true
` + lvmBlock + `d-i partman-auto/expert_recipe string \
    multiraid :: \
    512 512 640 free $bootable{ } method{ efi } format{ } . \
    512 512 512 raid $primary{ } method{ raid } . \
    1000 10000 -1 raid $primary{ } method{ raid } . \
    2048 4096 -1 xfs $defaultignore{ } $lvmok{ } method{ format } format{ } use_filesystem{ } filesystem{ xfs } mountpoint{ / } .
d-i partman-auto-raid/recipe string \
    1 2 0 ext4 /boot /dev/sda2#/dev/sdb2 . \
    1 2 0 lvm - /dev/sda3#/dev/sdb3 .
d-i mdadm/boot_degraded boolean true
d-i grub-installer/force-efi-extra-removable boolean true
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
` + partmanTail + "d-i grub-installer/bootdev string /dev/sda /dev/sdb\n" + espSyncSdaSdb,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := translate(t, c.src); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

// TestTranslateDebianConfigMirrorNeedsTwoDevices: coherence (design §6.5).
func TestTranslateDebianConfigMirrorNeedsTwoDevices(t *testing.T) {
	if _, err := translateDebianConfig([]byte("disk:\n  devices: [/dev/sda]\n  raid: mirror\n")); err == nil {
		t.Error("raid: mirror with < 2 devices must be rejected")
	}
}

// TestTranslateDebianConfigBootDegradedFalse: the operator can opt out.
func TestTranslateDebianConfigBootDegradedFalse(t *testing.T) {
	got := translate(t, "disk:\n  devices: [/dev/sda, /dev/sdb]\n  raid: mirror\n  boot_degraded: false\n")
	if !strings.Contains(got, "d-i mdadm/boot_degraded boolean false\n") {
		t.Errorf("boot_degraded: false not emitted:\n%s", got)
	}
}

// TestPartitionName: udev inserts a "p" separator when the device name ends
// in a digit (nvme0n1 -> nvme0n1p1); classic sdX concatenates.
func TestPartitionName(t *testing.T) {
	if got := partitionName("/dev/sda", 2); got != "/dev/sda2" {
		t.Errorf("sda partition = %q, want /dev/sda2", got)
	}
	if got := partitionName("/dev/nvme0n1", 1); got != "/dev/nvme0n1p1" {
		t.Errorf("nvme partition = %q, want /dev/nvme0n1p1", got)
	}
	if got := memberParts([]string{"/dev/nvme0n1", "/dev/nvme1n1"}, 2); got != "/dev/nvme0n1p2#/dev/nvme1n1p2" {
		t.Errorf("memberParts = %q", got)
	}
}

// TestTranslateDebianConfigSSHKeysLateCommand: ssh_authorized_keys has no
// native preseed directive (M3) — it lowers to an in-target late_command
// block (mkdir, append keys, chown/chmod), golden-pinned.
func TestTranslateDebianConfigSSHKeysLateCommand(t *testing.T) {
	src := `accounts:
  user:
    username: ops
    password_hash: $6$h
    ssh_authorized_keys:
      - ssh-ed25519 AAAA key1
      - ssh-ed25519 BBBB key2
`
	got := translate(t, src)
	want := `d-i passwd/root-login boolean false
d-i passwd/make-user boolean true
d-i passwd/user-fullname string ops
d-i passwd/username string ops
d-i passwd/user-password-crypted password $6$h
d-i preseed/late_command string in-target mkdir -p /home/ops/.ssh ; in-target sh -c 'printf "%s\n" "ssh-ed25519 AAAA key1" "ssh-ed25519 BBBB key2" >> /home/ops/.ssh/authorized_keys' ; in-target chown -R ops:ops /home/ops/.ssh ; in-target chmod 700 /home/ops/.ssh ; in-target chmod 600 /home/ops/.ssh/authorized_keys
`
	if got != want {
		t.Errorf("ssh keys:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTranslateDebianConfigSSHKeyQuoteRejected: a key containing a single
// quote would escape the sh -c quoting — reject it (coherence, 422 upstream).
func TestTranslateDebianConfigSSHKeyQuoteRejected(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n    ssh_authorized_keys: [\"bad'key\"]\n"
	if _, err := translateDebianConfig([]byte(src)); err == nil {
		t.Error("key containing a single quote must be rejected")
	}
}

// TestTranslateDebianConfigSSHKeyDoubleQuoteRejected: each key is interpolated
// as a DOUBLE-quoted printf argument (sshLateCommand) — a '"' in the key
// closes that argument early and injects shell syntax. Reject it.
func TestTranslateDebianConfigSSHKeyDoubleQuoteRejected(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n    ssh_authorized_keys: ['bad\"key']\n"
	if _, err := translateDebianConfig([]byte(src)); err == nil {
		t.Error("key containing a double quote must be rejected")
	}
}

// TestTranslateDebianConfigSSHKeyCommandSubstitutionRejected: '$' triggers
// shell expansion inside the double-quoted printf argument — a key like
// "$(touch /tmp/pwned)" would execute arbitrary commands as root during
// install (in-target). Reject it.
func TestTranslateDebianConfigSSHKeyCommandSubstitutionRejected(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n    ssh_authorized_keys: ['$(touch /tmp/pwned)']\n"
	if _, err := translateDebianConfig([]byte(src)); err == nil {
		t.Error("key containing $(...) command substitution must be rejected")
	}
}

// TestTranslateDebianConfigSSHKeyBacktickRejected: backticks are legacy
// command substitution syntax, live inside a double-quoted shell argument
// just like $(...). Reject it.
func TestTranslateDebianConfigSSHKeyBacktickRejected(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n    ssh_authorized_keys: ['`id`']\n"
	if _, err := translateDebianConfig([]byte(src)); err == nil {
		t.Error("key containing backticks must be rejected")
	}
}

// TestTranslateDebianConfigSSHKeyEmptyRejected: an empty/whitespace-only key
// would printf a blank line into authorized_keys — reject it.
func TestTranslateDebianConfigSSHKeyEmptyRejected(t *testing.T) {
	src := "accounts:\n  user:\n    username: ops\n    password_hash: $6$h\n    ssh_authorized_keys: ['']\n"
	if _, err := translateDebianConfig([]byte(src)); err == nil {
		t.Error("empty key must be rejected")
	}
}

// TestTranslateDebianConfigSSHThenESPSyncOrder pins the 2-source late_command
// order for a UEFI mirror with ssh keys: the ssh block comes FIRST, then the
// ESP-sync (' ; '-joined onto ONE d-i late_command line). Task 7 appends the
// operator source after these two.
func TestTranslateDebianConfigSSHThenESPSyncOrder(t *testing.T) {
	src := `accounts:
  user:
    username: ops
    password_hash: $6$h
    ssh_authorized_keys: [ssh-ed25519 AAAA k1]
disk:
  devices: [/dev/sda, /dev/sdb]
  raid: mirror
`
	got := translate(t, src)
	sshFrag := "in-target mkdir -p /home/ops/.ssh ; in-target sh -c 'printf \"%s\\n\" \"ssh-ed25519 AAAA k1\" >> /home/ops/.ssh/authorized_keys' ; in-target chown -R ops:ops /home/ops/.ssh ; in-target chmod 700 /home/ops/.ssh ; in-target chmod 600 /home/ops/.ssh/authorized_keys"
	espFrag := `in-target sh -c 'mkfs.vfat -F32 /dev/sdb1' ; in-target sh -c 'mount /dev/sdb1 /mnt && cp -a /boot/efi/. /mnt/ && umount /mnt' ; in-target sed -i -E '\| /boot/efi |s|(vfat[[:space:]]+)([^[:space:]]+)|\1\2,nofail,x-systemd.device-timeout=1|' /etc/fstab`
	wantLate := "d-i preseed/late_command string " + sshFrag + " ; " + espFrag + "\n"
	if !strings.Contains(got, wantLate) {
		t.Errorf("ssh-then-ESP-sync order not pinned:\ngot:\n%s\nwant late line:\n%s", got, wantLate)
	}
}

// TestTranslateDebianConfigEscapeHatchOrdering pins the composition contract:
// curated lines, then ONE composed late_command (ssh fragment before the
// operator's, "; "-joined, operator newlines flattened), then raw_preseed
// verbatim LAST (later duplicate debconf answers win — the hatch can always
// override a curated line).
func TestTranslateDebianConfigEscapeHatchOrdering(t *testing.T) {
	src := `hostname: node1
accounts:
  user:
    username: ops
    password_hash: $6$h
    ssh_authorized_keys: [ssh-ed25519 AAAA k1]
late_command: |
  in-target systemctl enable ssh
  in-target apt-get clean
raw_preseed: |
  d-i debian-installer/allow_unauthenticated boolean true
  d-i netcfg/get_hostname string overridden
`
	got := translate(t, src)
	want := `d-i netcfg/get_hostname string node1
d-i passwd/root-login boolean false
d-i passwd/make-user boolean true
d-i passwd/user-fullname string ops
d-i passwd/username string ops
d-i passwd/user-password-crypted password $6$h
d-i preseed/late_command string in-target mkdir -p /home/ops/.ssh ; in-target sh -c 'printf "%s\n" "ssh-ed25519 AAAA k1" >> /home/ops/.ssh/authorized_keys' ; in-target chown -R ops:ops /home/ops/.ssh ; in-target chmod 700 /home/ops/.ssh ; in-target chmod 600 /home/ops/.ssh/authorized_keys ; in-target systemctl enable ssh ; in-target apt-get clean
d-i debian-installer/allow_unauthenticated boolean true
d-i netcfg/get_hostname string overridden
`
	if got != want {
		t.Errorf("ordering:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTranslateDebianConfigLateCommandAlone: an operator late_command with no
// ssh keys still emits (flattened to one line).
func TestTranslateDebianConfigLateCommandAlone(t *testing.T) {
	got := translate(t, "late_command: |\n  in-target systemctl enable ssh\n")
	want := "d-i preseed/late_command string in-target systemctl enable ssh\n"
	if got != want {
		t.Errorf("late alone:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestTranslateDebianConfigExpertRecipeReplacesCuratedDisk: expert_recipe is
// emitted verbatim and REPLACES the curated recipe (design §6.4) — the
// layout/filesystem/raid knobs are ignored AND bypass curated-knob validation;
// devices and boot_degraded still apply.
func TestTranslateDebianConfigExpertRecipeReplacesCuratedDisk(t *testing.T) {
	src := `disk:
  devices: [/dev/sda]
  layout: bogus-ignored
  boot_degraded: true
  expert_recipe: |
    boot-root ::
    512 512 512 ext4 $primary{ } method{ format } format{ } use_filesystem{ } filesystem{ ext4 } mountpoint{ /boot } .
`
	got := translate(t, src)
	want := `d-i partman-auto/disk string /dev/sda
d-i partman-auto/method string regular
d-i partman-auto/expert_recipe string boot-root :: 512 512 512 ext4 $primary{ } method{ format } format{ } use_filesystem{ } filesystem{ ext4 } mountpoint{ /boot } .
d-i mdadm/boot_degraded boolean true
` + partmanTail + "d-i grub-installer/bootdev string /dev/sda\n"
	if got != want {
		t.Errorf("expert recipe:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTranslateDebianConfigExpertRecipeStillNeedsDevices: the one check that
// survives the expert bypass.
func TestTranslateDebianConfigExpertRecipeStillNeedsDevices(t *testing.T) {
	if _, err := translateDebianConfig([]byte("disk:\n  expert_recipe: |\n    x .\n")); err == nil {
		t.Error("expert_recipe without devices must be rejected")
	}
}

// TestTranslateDebianConfigCombinedEndToEnd pins the WHOLE emission-order
// contract in one golden (I3): network(static) + mirror (with the B2
// mirror/country manual line) + accounts(root+user+ssh) + packages + a UEFI
// raid: mirror disk + the full 3-source composed late_command (ssh -> ESP-sync
// -> operator) + raw_preseed LAST. The disk is deliberately a UEFI mirror so
// all three late_command sources are pinned together in order. This is the
// single place the cross-section ordering is asserted end-to-end.
func TestTranslateDebianConfigCombinedEndToEnd(t *testing.T) {
	src := `hostname: node1
domain: cluster.local
locale: en_US.UTF-8
timezone: Etc/UTC
keyboard: us
mirror:
  hostname: deb.debian.org
  directory: /debian
network:
  interface: auto
  static:
    address: 10.0.0.10
    netmask: 255.255.255.0
    gateway: 10.0.0.1
    nameservers: [10.0.0.1]
accounts:
  root_password_hash: $6$root
  user:
    username: ops
    password_hash: $6$user
    ssh_authorized_keys: [ssh-ed25519 AAAA k1]
packages: [openssh-server]
disk:
  devices: [/dev/sda, /dev/sdb]
  raid: mirror
late_command: |
  in-target systemctl enable ssh
raw_preseed: |
  d-i debian-installer/allow_unauthenticated boolean true
`
	want := `d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/disable_autoconfig boolean true
d-i netcfg/get_ipaddress string 10.0.0.10
d-i netcfg/get_netmask string 255.255.255.0
d-i netcfg/get_gateway string 10.0.0.1
d-i netcfg/get_nameservers string 10.0.0.1
d-i netcfg/confirm_static boolean true
d-i netcfg/get_hostname string node1
d-i netcfg/get_domain string cluster.local
d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i time/zone string Etc/UTC
d-i passwd/root-login boolean true
d-i passwd/root-password-crypted password $6$root
d-i passwd/make-user boolean true
d-i passwd/user-fullname string ops
d-i passwd/username string ops
d-i passwd/user-password-crypted password $6$user
d-i partman-auto/disk string /dev/sda /dev/sdb
d-i partman-auto/method string raid
d-i partman-efi/non_efi_system boolean true
d-i partman-md/device_remove_md boolean true
d-i partman-auto/expert_recipe string \
    multiraid :: \
    512 512 640 free $bootable{ } method{ efi } format{ } . \
    512 512 512 raid $primary{ } method{ raid } . \
    1000 10000 -1 raid $primary{ } method{ raid } .
d-i partman-auto-raid/recipe string \
    1 2 0 ext4 /boot /dev/sda2#/dev/sdb2 . \
    1 2 0 ext4 / /dev/sda3#/dev/sdb3 .
d-i mdadm/boot_degraded boolean true
d-i grub-installer/force-efi-extra-removable boolean true
d-i partman-md/confirm boolean true
d-i partman-md/confirm_nooverwrite boolean true
` + partmanTail + `d-i grub-installer/bootdev string /dev/sda /dev/sdb
d-i pkgsel/include string openssh-server
d-i preseed/late_command string in-target mkdir -p /home/ops/.ssh ; in-target sh -c 'printf "%s\n" "ssh-ed25519 AAAA k1" >> /home/ops/.ssh/authorized_keys' ; in-target chown -R ops:ops /home/ops/.ssh ; in-target chmod 700 /home/ops/.ssh ; in-target chmod 600 /home/ops/.ssh/authorized_keys ; in-target sh -c 'mkfs.vfat -F32 /dev/sdb1' ; in-target sh -c 'mount /dev/sdb1 /mnt && cp -a /boot/efi/. /mnt/ && umount /mnt' ; in-target sed -i -E '\| /boot/efi |s|(vfat[[:space:]]+)([^[:space:]]+)|\1\2,nofail,x-systemd.device-timeout=1|' /etc/fstab ; in-target systemctl enable ssh
d-i debian-installer/allow_unauthenticated boolean true
`
	if got := translate(t, src); got != want {
		t.Errorf("combined:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
