//go:build unix

package proxydhcp

import (
	"net"
	"syscall"
)

// enableBroadcast sets SO_BROADCAST so the UDP/67 socket may reply to the
// 255.255.255.255 broadcast address. Unix-only: booty targets Linux (the TFTP
// path already is) and ships a debian12 image; there is no Windows build.
func enableBroadcast(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); ctrlErr != nil {
		return ctrlErr
	}
	return setErr
}
