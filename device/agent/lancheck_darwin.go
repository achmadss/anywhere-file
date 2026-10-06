package main

import (
	"errors"
	"net"
	"syscall"

	"golang.org/x/net/ipv4"
)

// lanBlocked sends an empty mDNS message, a header with no questions that every responder
// ignores, to the group. macOS has no public way to read the Local Network permission, but
// with it denied a send to the LAN fails with "no route to host" while a send with no
// network at all fails with "network is unreachable" (Apple's TN3179). So only the first
// counts.
func lanBlocked() lanProblem {
	iface := multicastInterface()
	if iface == nil {
		return lanFine
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return lanFine
	}
	defer c.Close()
	_ = ipv4.NewPacketConn(c).SetMulticastInterface(iface)
	if _, err := c.WriteToUDP(make([]byte, 12), mdnsGroup4); errors.Is(err, syscall.EHOSTUNREACH) {
		return lanDenied
	}
	return lanFine
}
