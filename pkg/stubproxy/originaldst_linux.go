//go:build linux

package stubproxy

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const soOriginalDst = 80

func originalDst(conn *net.TCPConn) (string, int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", 0, err
	}

	var addr syscall.RawSockaddrInet4
	var opErr error

	err = raw.Control(func(fd uintptr) {
		l := uint32(unsafe.Sizeof(addr))
		_, _, errno := unix.Syscall6(
			unix.SYS_GETSOCKOPT,
			fd,
			uintptr(unix.SOL_IP),
			uintptr(soOriginalDst),
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&l)),
			0,
		)
		if errno != 0 {
			opErr = errno
		}
	})

	if err != nil {
		return "", 0, err
	}
	if opErr != nil {
		return "", 0, opErr
	}

	ip := fmt.Sprintf("%d.%d.%d.%d", addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3])
	port := int(addr.Port>>8) | int(addr.Port&0xff)<<8
	return ip, port, nil
}
