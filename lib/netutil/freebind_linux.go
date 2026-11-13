//go:build linux

package netutil

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// freeBindOSNote is a platform-specific part of the -freeBindAddress flag description.
const freeBindOSNote = "This flag is supported only on Linux OSes."

// newFreebindListenConfig returns a net.ListenConfig, which sets the IP_FREEBIND
// socket option before the socket is bound to the listening addr,
// so that the socket can be bound to an addr, which doesn't exist yet on the host.
// See https://lwn.net/Articles/542907/
//
// The option is set on the listening socket only,
// so the accepted connections aren't affected by it.
//
// The returned ListenConfig is zero-value if the -freeBindAddress
// command-line flag isn't set.
func newFreebindListenConfig() net.ListenConfig {
	var lc net.ListenConfig
	if !*freeBindAddress {
		return lc
	}
	lc.Control = func(network, _ string, c syscall.RawConn) error {
		var ctrlErr error
		if err := c.Control(func(fd uintptr) {
			ctrlErr = setFreebindOption(network, int(fd))
		}); err != nil {
			return fmt.Errorf("cannot access the underlying socket descriptor: %w", err)
		}
		return ctrlErr
	}
	return lc
}

// NewUDPListenPacket returns a net.PacketConn bound to the given network and addr.
//
// If the -freeBindAddress command-line flag is set, then the returned socket
// has the IP_FREEBIND option set, so it can be bound to an IPv4 addr,
// which doesn't exist yet on the host.
func NewUDPListenPacket(network, addr string) (net.PacketConn, error) {
	lc := newFreebindListenConfig()
	return lc.ListenPacket(context.Background(), network, addr)
}

// setFreebindOption sets the IP_FREEBIND socket option on the given file descriptor,
// so the socket can be bound to an IPv4 addr, which doesn't exist yet on the host.
// It sets the IPV6_FREEBIND socket option instead if the given network is IPv6.
func setFreebindOption(network string, fd int) error {
	level := unix.IPPROTO_IP
	option := unix.IP_FREEBIND
	if network == "tcp6" || network == "udp6" {
		level = unix.IPPROTO_IPV6
		option = unix.IPV6_FREEBIND
	}
	if err := unix.SetsockoptInt(fd, level, option, 1); err != nil {
		return fmt.Errorf("cannot set freebind socket option on fd %d: %w", fd, err)
	}
	return nil
}
