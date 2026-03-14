//go:build linux

package stubproxy

import (
	"net"
	"syscall"
	"unsafe"
)

const soOriginalDst = 80

func originalDst(conn *net.TCPConn) (string, int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", 0, err
	}
	var addr syscall.RawSockaddrInet4
	var opErr error
	raw.Control(func(fd uintptr) {
		l := uint32(unsafe.Sizeof(addr))
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			uintptr(syscall.SOL_IP),
			uintptr(soOriginalDst),
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&l)),
			0,
		)
		if errno != 0 {
			opErr = errno
		}
	})
	if opErr != nil {
		return "", 0, opErr
	}
	ip := net.IPv4(addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3]).String()
	port := int(addr.Port>>8) | int(addr.Port&0xff)<<8
	return ip, port, nil
}
