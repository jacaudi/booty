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
	set BASEURL [[talos-baseurl]]
	kernel ${BASEURL}/kernel-[[talos-arch]] talos.platform=metal slab_nomerge pti=on talos.config=http://[[server]]/machineconfig?uuid=${uuid}&serial=${serial}&mac=${mac}&hostname=${hostname}
	initrd ${BASEURL}/initramfs-[[talos-arch]].xz
	boot`

	PXEConfig["flatcar.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL [[flatcar-baseurl]]
	kernel ${BASEURL}/flatcar_production_pxe.vmlinuz flatcar.first_boot=1 ignition.config.url=http://[[server]]/ignition.json
	initrd ${BASEURL}/flatcar_production_pxe_image.cpio.gz
	boot`

	PXEConfig["coreos.ipxe"] = `#!ipxe
	echo Hello from Booty!
	set BASEURL [[coreos-baseurl]]
	set CONFIGURL http://[[server]]/ignition.json
	set STREAM [[coreos-channel]]
	set VERSION [[coreos-version]]
	set ARCH [[coreos-arch]]

	kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel-${ARCH} enforcing=0 initrd=main coreos.live.rootfs_url=${BASEURL}/fedora-coreos-${VERSION}-live-rootfs.${ARCH}.img ignition.firstboot ignition.platform.id=metal ignition.firstboot=1 ignition.config.url=${CONFIGURL}
	initrd --name main ${BASEURL}/fedora-coreos-${VERSION}-live-initramfs.${ARCH}.img
	boot`

	PXEConfig["holding.ipxe"] = `#!ipxe
	echo Booty: this host is not yet approved. Waiting...
	sleep 30
	chain http://[[server]]/booty.ipxe || shell`
}
