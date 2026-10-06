package main

// The mDNS responder (#151). hashicorp/mdns builds the records, and its Server would answer
// them, but that Server sends every answer by unicast to the port the query came from. A
// querier sharing 5353 with Bonjour, Avahi or the Windows resolver then never sees it,
// because the kernel hands a unicast packet to one socket on the port. This one answers on
// the group unless the query asks for unicast, which is what RFC 6762 says.

import (
	"errors"
	"log/slog"
	"net"

	"github.com/hashicorp/mdns"
	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const mdnsPort = 5353

var (
	mdnsGroup4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}
	mdnsGroup6 = &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort}
)

type responder struct {
	zone  mdns.Zone
	log   *slog.Logger
	conns []*net.UDPConn
}

// newResponder binds 5353 on IPv4 and IPv6 and answers from zone until close. One family
// failing is fine; both failing is an error.
func newResponder(zone mdns.Zone, iface *net.Interface, log *slog.Logger) (*responder, error) {
	r := &responder{zone: zone, log: log}
	// ListenMulticastUDP sets SO_REUSEADDR on every platform and SO_REUSEPORT on the BSDs,
	// which lets the agent bind beside the system responder. It also turns multicast
	// loopback off, and a querier on this PC needs it on to hear the answer.
	if c, err := net.ListenMulticastUDP("udp4", iface, mdnsGroup4); err == nil {
		p := ipv4.NewPacketConn(c)
		_ = p.SetMulticastLoopback(true)
		// RFC 6762 section 11: sent with TTL 255.
		_ = p.SetMulticastTTL(255)
		r.serve(c, mdnsGroup4)
	}
	if c, err := net.ListenMulticastUDP("udp6", iface, mdnsGroup6); err == nil {
		p := ipv6.NewPacketConn(c)
		_ = p.SetMulticastLoopback(true)
		_ = p.SetMulticastHopLimit(255)
		r.serve(c, mdnsGroup6)
	}
	if len(r.conns) == 0 {
		return nil, errors.New("no multicast listeners could be started")
	}
	return r, nil
}

func (r *responder) serve(c *net.UDPConn, group *net.UDPAddr) {
	r.conns = append(r.conns, c)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				continue
			}
			var query dns.Msg
			if query.Unpack(buf[:n]) != nil {
				continue
			}
			multi, uni := answer(r.zone, &query, from)
			r.send(c, multi, group)
			r.send(c, uni, from)
		}
	}()
}

func (r *responder) send(c *net.UDPConn, m *dns.Msg, to *net.UDPAddr) {
	if m == nil {
		return
	}
	buf, err := m.Pack()
	if err == nil {
		_, err = c.WriteToUDP(buf, to)
	}
	if err != nil {
		r.log.Debug("mdns: answer not sent", "to", to, "err", err)
	}
}

func (r *responder) close() {
	for _, c := range r.conns {
		_ = c.Close()
	}
}

// answer splits what zone has for query into the answer for the group and the answer for
// the sender. Either can be nil.
//
// A question with the top bit of qclass set asks for unicast (RFC 6762 section 5.4). A query
// from a port other than 5353 is a legacy query (section 6.7): the sender is not listening
// on the group, so it gets everything by unicast, with its query id and questions echoed.
// The desktop client and `agent discover` both ask that way.
func answer(zone mdns.Zone, query *dns.Msg, from *net.UDPAddr) (multicast, unicast *dns.Msg) {
	// Section 18: a query has QR, OPCODE and RCODE zero, and anything else is ignored. Our
	// own answers come back to us through loopback, and this drops them.
	if query.Response || query.Opcode != dns.OpcodeQuery || query.Rcode != 0 {
		return nil, nil
	}
	legacy := from.Port != mdnsPort
	var multi, uni []dns.RR
	for _, q := range query.Question {
		records := zone.Records(q)
		if legacy || q.Qclass&(1<<15) != 0 {
			uni = append(uni, records...)
		} else {
			multi = append(multi, records...)
		}
	}
	reply := func(id uint16, rrs []dns.RR) *dns.Msg {
		if len(rrs) == 0 {
			return nil
		}
		return &dns.Msg{
			MsgHdr:   dns.MsgHdr{Id: id, Response: true, Opcode: dns.OpcodeQuery, Authoritative: true},
			Compress: true,
			Answer:   rrs,
		}
	}
	// Section 18.1: the id is zero on the group and the query's own in a unicast answer.
	multicast = reply(0, multi)
	unicast = reply(query.Id, uni)
	if unicast != nil && legacy {
		unicast.Question = query.Question
	}
	return multicast, unicast
}
