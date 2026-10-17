package bootscript

import "github.com/jeefy/booty/pkg/bootticket"

// ticketParam is the config-URL parameter that carries the boot ticket. Every
// template puts it directly after mac=, before any variable iPXE can expand to
// text with a space (${serial}, ${hostname}), which would end the kernel
// parameter and drop it (design R12).
const ticketParam = "&" + bootticket.QueryKey + "=[[ticket]]"

var PXEConfig map[string]string

func init() {
	PXEConfig = make(map[string]string)
	PXEConfig["talos.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL [[talos-baseurl]]
	kernel ${BASEURL}/kernel-[[talos-arch]] talos.platform=metal slab_nomerge pti=on talos.config=http://[[server]]/machineconfig?mac=${mac}` + ticketParam + `&uuid=${uuid}&serial=${serial}&hostname=${hostname}
	initrd ${BASEURL}/initramfs-[[talos-arch]].xz
	boot`

	PXEConfig["flatcar.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL [[flatcar-baseurl]]
	kernel ${BASEURL}/flatcar_production_pxe.vmlinuz flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json?mac=${mac}` + ticketParam + `
	initrd ${BASEURL}/flatcar_production_pxe_image.cpio.gz
	boot`

	PXEConfig["coreos.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL [[coreos-baseurl]]
	set CONFIGURL http://[[server]]/ignition.json?mac=${mac}` + ticketParam + `
	set VERSION [[coreos-version]]
	set ARCH [[coreos-arch]]

	kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel.${ARCH} enforcing=0 initrd=main coreos.live.rootfs_url=${BASEURL}/fedora-coreos-${VERSION}-live-rootfs.${ARCH}.img ignition.firstboot ignition.platform.id=metal ignition.firstboot=1 ignition.config.url=${CONFIGURL}
	initrd --name main ${BASEURL}/fedora-coreos-${VERSION}-live-initramfs.${ARCH}.img
	boot`

	PXEConfig["holding.ipxe"] = `#!ipxe
	echo Booty: this host is not yet approved. Waiting...
	sleep 30
	chain http://[[server]]` + ChainPath + ` || shell`

	// Unlike the other OS templates, the boot URL is substituted directly into
	// the kernel/initrd lines rather than via an iPXE "set BASEURL"/${BASEURL}
	// indirection: [[debian-baseurl]] is already fully resolved server-side
	// before the script is sent, and the direct form keeps the rendered script
	// simpler with no runtime-only variable for iPXE to expand.
	PXEConfig["debian.ipxe"] = `#!ipxe
	echo Booting Debian installer from Booty
	kernel [[debian-baseurl]]/linux auto=true priority=critical preseed/url=http://[[server]]/preseed?mac=${mac}` + ticketParam + ` vga=788 --- quiet
	initrd [[debian-baseurl]]/initrd.gz
	boot`
}
