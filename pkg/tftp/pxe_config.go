package tftp

var PXEConfig map[string]string

func init() {
	PXEConfig = make(map[string]string)
	PXEConfig["flatcar"] = `default flatcar
	prompt 1
	timeout 5
	
	display boot.msg
	
	label flatcar
		menu default
		kernel flatcar_production_pxe.vmlinuz
		initrd flatcar_production_pxe_image.cpio.gz
		append flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json`

	PXEConfig["talos.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL http://[[server]]/data/cache/talos/[[talos-schematic]]/[[talos-arch]]/[[talos-version]]
	kernel ${BASEURL}/kernel-[[talos-arch]] talos.platform=metal slab_nomerge pti=on talos.config=http://[[server]]/machineconfig?uuid=${uuid}&serial=${serial}&mac=${mac}&hostname=${hostname}
	initrd ${BASEURL}/initramfs-[[talos-arch]].xz
	boot`

	PXEConfig["flatcar.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL http://[[server]]/data/cache/flatcar/-/[[flatcar-arch]]/[[flatcar-version]]
	kernel ${BASEURL}/flatcar_production_pxe.vmlinuz flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json
	initrd ${BASEURL}/flatcar_production_pxe_image.cpio.gz
	boot`

	PXEConfig["flatcar_booty.ipxe"] = `#!ipxe
	echo "Hello from Booty!"
	set BASEURL http://[[server]]/data/
	set CONFIGURL http://[[server]]/ignition.json
	set menu-default [[menu-default]]
	chain http://[[server]]/data/flatcar_booty.ipxe
	boot`

	PXEConfig["coreos.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL http://[[server]]/data/cache/coreos/-/[[coreos-arch]]/[[coreos-version]]
	set CONFIGURL http://[[server]]/ignition.json
	set STREAM [[coreos-channel]]
	set VERSION [[coreos-version]]
	set ARCH [[coreos-arch]]

	kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel-${ARCH} enforcing=0 initrd=main coreos.live.rootfs_url=${BASEURL}/fedora-coreos-${VERSION}-live-rootfs.${ARCH}.img ignition.firstboot ignition.platform.id=metal ignition.firstboot=1 ignition.config.url=${CONFIGURL}
	initrd --name main ${BASEURL}/fedora-coreos-${VERSION}-live-initramfs.${ARCH}.img
	boot`
}
