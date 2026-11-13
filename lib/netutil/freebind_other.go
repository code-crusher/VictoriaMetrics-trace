//go:build !linux

package netutil

import "net"

// freeBindOSNote is a platform-specific part of the -freeBindAddress flag description.
const freeBindOSNote = "This flag is supported only on Linux OSes; it is ignored on other OSes."

// newFreebindListenConfig returns an empty net.ListenConfig on non-Linux platforms,
// since the IP_FREEBIND socket option is Linux-specific.
func newFreebindListenConfig() net.ListenConfig {
	return net.ListenConfig{}
}

// NewUDPListenPacket returns a net.PacketConn bound to the given network and addr.
//
// The -freeBindAddress command-line flag is ignored on non-Linux platforms,
// since the IP_FREEBIND socket option is Linux-specific.
func NewUDPListenPacket(network, addr string) (net.PacketConn, error) {
	return net.ListenPacket(network, addr)
}
