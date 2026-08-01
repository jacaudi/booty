package tftp

// Literal iPXE scripts for netboot.xyz-sourced tools, one per tool, pinned from
// netboot.xyz's own menu templates. They are literal rather than generated from
// a shared "boot form" because the tools differ STRUCTURALLY, not just in their
// cmdline: SystemRescue needs two initrd lines, memtest86plus boots a single
// binary, UEFI Shell is EFI-only. A form abstraction could not express that, and
// this matches the existing PXEConfig["debian.ipxe"] idiom.
//
// [[baseurl]] resolves to the cached version directory (see bootTokensFor).
// Scripts are flush-left rather than tab-indented like the older entries in
// pxe_config.go: iPXE is whitespace-tolerant, and leading tabs in those entries
// are an accident of Go's raw strings, not a convention worth propagating.
func init() {
	// Upstream: roles/netbootxyz/templates/menu/systemrescue.ipxe.j2 :boot,
	// with ${ipparam} and kernel_params expanded from defaults/main.yml.
	//
	// initrd=initrd.magic is an iPXE UEFI mechanism, NOT a kernel or archiso
	// one (ipxe commit e5f0255): iPXE's EFI build synthesizes a file of that
	// name containing every loaded initrd concatenated, because the Linux EFI
	// stub loads only the single file named by initrd=. It is:
	//   - a provable no-op on BIOS (bzimage.c never reads initrd=; the kernel's
	//     memparse rejects the value),
	//   - never read on iPXE >= Feb 2023, which serves the same blob via
	//     EFI_LOAD_FILE2_PROTOCOL and the stub prefers that,
	//   - LOAD-BEARING on older iPXE under UEFI, where its absence means no
	//     initrd is loaded at all.
	// booty does not ship iPXE — the operator stages undionly.kpxe/ipxe.efi
	// themselves — so the build is unknown and the cheap insurance is kept.
	// If a filename is ever named here it must be initrd.magic: "initrd=initrd"
	// would load only the first cpio and silently drop the hook below.
	//
	// BOOTIF's ENTIRE effect is filling the device field of klibc's ip= string
	// (mkinitcpio-archiso hooks/archiso_pxe_common:13-30), so it matters only on
	// multi-NIC hosts; on a single NIC "pin to X" and "race among {X}" are the
	// same thing. Note it is INERT without ip= on the cmdline (the hook gates on
	// it), and an unmatched value degrades to race-all rather than failing.
	// ${netX} is an iPXE built-in resolving to last_opened_netdev() — not a
	// netboot.xyz variable — so it needs no preamble in a standalone script.
	// The bare (non-01-/hexhyp) form is upstream's and is what archiso's
	// ${BOOTIF#01-} + tr both no-op on; kept deliberately over the canonical
	// PXELINUX form, which would buy portability to an initramfs we do not have.
	//
	// archiso_http_srv has NO trailing slash: the netboot.xyz-patched
	// archiso_pxe_http hook fetches "${archiso_http_srv}/airootfs.sfs", so a
	// trailing slash yields a double slash that only resolves via booty's
	// ServeMux 301 redirect.
	PXEConfig["systemrescue.ipxe"] = `#!ipxe
echo Booting SystemRescue from Booty
imgfree
kernel [[baseurl]]/vmlinuz archisobasedir=sysresccd BOOTIF=${netX/mac} ip=dhcp net.ifnames=0 archiso_http_srv=[[baseurl]] initrd=initrd.magic
initrd [[baseurl]]/initrd
initrd [[baseurl]]/archiso_pxe_http /hooks/archiso_pxe_http mode=755
boot`

	// Upstream type "direct" (utils-efi.ipxe.j2): imgfree + kernel + boot.
	// EFI-only by construction, so fail readably on BIOS. The guard branch
	// re-chains booty.ipxe rather than ending, so control never falls through
	// into whatever label the generated menu places next.
	PXEConfig["uefi-shell.ipxe"] = `#!ipxe
iseq ${platform} efi || goto notefi
echo Booting UEFI Shell from Booty
imgfree
kernel [[baseurl]]/uefi-shell-x64.efi
boot
:notefi
echo UEFI Shell requires an EFI client; this machine booted in BIOS mode.
sleep 10
chain tftp://[[server-ip]]/booty.ipxe || shell`

	// Upstream type "memtest": utils-efi and utils-pcbios-64 BOTH resolve
	// util_path to mt86p_x86_64, so no firmware branch is needed on amd64
	// (mt86p_i586 is the 32-bit build, a different booty arch). The endpoint
	// ships seven files; only this one is booted.
	PXEConfig["memtest86plus.ipxe"] = `#!ipxe
echo Booting Memtest86+ from Booty
imgfree
kernel [[baseurl]]/mt86p_x86_64
boot`

	// Upstream: menu/clonezilla.ipxe.j2 :clonezilla-boot. filesystem.squashfs is
	// NOT loaded as an initrd — the live-boot initramfs fetches it over HTTP
	// from the fetch= URL, so it must be cached but never appears on an initrd
	// line. booty serves it from an IP, which is why netbootxyz#1254 (an open
	// DNS race in the same fetch= path) cannot affect us.
	PXEConfig["clonezilla.ipxe"] = `#!ipxe
echo Booting Clonezilla from Booty
imgfree
kernel [[baseurl]]/vmlinuz boot=live username=user union=overlay config components noswap edd=on nomodeset ocs_live_run="ocs-live-general" ocs_live_batch=no net.ifnames=0 nosplash noprompt fetch=[[baseurl]]/filesystem.squashfs initrd=initrd.magic
initrd [[baseurl]]/initrd
boot`

	// Upstream: defaults/main.yml rescatux (type: direct), identical under
	// utilitiesefi and utilitiespcbios64 — hence no platform guard. Do NOT
	// derive this from clonezilla.ipxe.j2: Clonezilla carries seven options
	// Rescatux does not, and Rescatux carries the SELinux trio below, which a
	// Clonezilla derivation silently drops.
	PXEConfig["rescatux.ipxe"] = `#!ipxe
echo Booting Rescatux from Booty
imgfree
kernel [[baseurl]]/vmlinuz boot=live fetch=[[baseurl]]/filesystem.squashfs selinux=1 security=selinux enforcing=0 initrd=initrd.magic
initrd [[baseurl]]/initrd
boot`

	// Upstream: defaults/main.yml zfsbootmenu (type: direct) — imgfree + kernel
	// + boot, EFI-only. Same shape and guard as uefi-shell.ipxe; the :notefi
	// block is LAST so control can never fall into the boot path.
	PXEConfig["zfsbootmenu.ipxe"] = `#!ipxe
iseq ${platform} efi || goto notefi
echo Booting ZFSBootMenu from Booty
imgfree
kernel [[baseurl]]/zfsbootmenu-recovery-x86_64.efi
boot
:notefi
echo ZFSBootMenu requires an EFI client; this machine booted in BIOS mode.
sleep 10
chain tftp://[[server-ip]]/booty.ipxe || shell`

	// Upstream: menu/shredos.ipxe.j2 :shredos_boot for the kernel line, plus its
	// warning menu ported here (design D12/§6.5).
	//
	// This boots a disk eraser. ShredOS lands in nwipe's INTERACTIVE interface
	// with disks listed and PRNG preselected — upstream's README is explicit that
	// it "does not autonuke your discs at launch", and booty does not pass
	// --autonuke. So this gate is defense in depth ahead of nwipe's own
	// confirmation, not the only thing between a keystroke and data loss.
	// Three properties are still load-bearing:
	//   1. The kernel/boot pair is the LAST thing in the file. iPXE executes
	//      sequentially, so if the safe arm's chain succeeds and later returns,
	//      anything placed after it would fall into the wipe. Do not append.
	//   2. choose defaults to the SAFE arm: timeout expiry selects the
	//      highlighted item, so an unattended machine must land on "back".
	//   3. The method is prng — a single-pass cryptographic-stream wipe, the
	//      fastest of the six upstream offers. A misclicked Gutmann pass would
	//      run for days.
	// Note iPXE's --retimeout defaults to 0, so ANY keypress makes the gate wait
	// indefinitely. That is fine: a parked gate is safe, an auto-proceeding one
	// is not. Upstream uses bare ${cmdline}, so there is deliberately no
	// initrd=initrd.magic here — ShredOS is kernel-only.
	PXEConfig["shredos.ipxe"] = `#!ipxe
menu ShredOS - disk eraser
item --gap This boots nwipe, which erases disks irreversibly.
item --gap You will still choose disks in nwipe; nothing is erased automatically.
item back Go back to the Booty menu (safe)
item wipe Continue to ShredOS
choose --timeout 300000 --default back sel || goto back
iseq ${sel} wipe && goto wipe || goto back
:back
chain tftp://[[server-ip]]/booty.ipxe || shell
exit 0
:wipe
echo Starting ShredOS. nwipe will list your disks; erasure is irreversible.
imgfree
kernel [[baseurl]]/shredos console=tty3 loglevel=3 nwipe_options="--method=prng"
boot`

	// Upstream: menu/live-tails.ipxe.j2 :boot. Three initrds — the base initrd,
	// netboot.xyz's patched live-boot helper, and the ISO itself mounted at
	// /tails.iso, which is what fromiso= consumes.
	//
	// Client RAM is 4-8 GB (netbootxyz#1102/#1104: the maintainer says 2 GB will
	// not do). Some VMs also need an emulated CD-ROM or the live filesystem is
	// not found (netbootxyz#1104) — that is a client-side quirk, not a booty bug.
	PXEConfig["tails.ipxe"] = `#!ipxe
echo Booting Tails from Booty
imgfree
kernel [[baseurl]]/vmlinuz boot=live fromiso=/tails.iso config nopersistence noprompt timezone=Etc/UTC splash noautologin module=Tails slab_nomerge slub_debug=FZP mce=0 vsyscall=none page_poison=1 init_on_free=1 mds=full,nosmt initrd=initrd.magic
initrd [[baseurl]]/initrd.img
initrd [[baseurl]]/9990-misc-helpers.sh /usr/lib/live/boot/9990-misc-helpers.sh
initrd [[baseurl]]/tails-amd64.iso /tails.iso
boot`
}
