package http

import (
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
