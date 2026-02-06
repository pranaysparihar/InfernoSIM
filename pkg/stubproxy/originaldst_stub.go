//go:build !linux

package stubproxy

import "net"

func originalDst(conn *net.TCPConn) (string, int, error) {
	return "", 0, errUnsupportedPlatform
}

var errUnsupportedPlatform = &unsupportedPlatformError{}

type unsupportedPlatformError struct{}

func (e *unsupportedPlatformError) Error() string {
	return "SO_ORIGINAL_DST not supported"
}
