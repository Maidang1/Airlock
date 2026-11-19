//go:build unix

package microvm

import (
	"net"
	"os"
	"syscall"
)

// CreateDatagramSocketPair creates an AF_UNIX SOCK_DGRAM socket pair sized for
// VZFileHandleNetworkDeviceAttachment and returns the host netstack Conn and
// the guest-side os.File to attach to VZ.
func CreateDatagramSocketPair() (netstackConn net.Conn, vzFile *os.File, err error) {
	pairs, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, err
	}
	closeBoth := func() {
		_ = syscall.Close(pairs[0])
		_ = syscall.Close(pairs[1])
	}
	for _, fd := range pairs {
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 1<<20); err != nil {
			closeBoth()
			return nil, nil, err
		}
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20); err != nil {
			closeBoth()
			return nil, nil, err
		}
	}
	serverFile := os.NewFile(uintptr(pairs[0]), "microvm-net-host")
	clientFile := os.NewFile(uintptr(pairs[1]), "microvm-net-vz")
	conn, err := net.FileConn(serverFile)
	_ = serverFile.Close()
	if err != nil {
		_ = clientFile.Close()
		return nil, nil, err
	}
	return conn, clientFile, nil
}
