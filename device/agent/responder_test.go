package main

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

func testService(t *testing.T) *mdns.MDNSService {
	t.Helper()
	svc, err := mdns.NewMDNSService("pc1", mdnsService, mdnsDomain, "af-test.local.", 7433, []net.IP{net.IPv4(192, 0, 2, 1)}, []string{"v=1"})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func ptrQuery(unicastBit bool) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(mdnsService+"."+mdnsDomain, dns.TypePTR)
	q.RecursionDesired = false
	if unicastBit {
		q.Question[0].Qclass |= 1 << 15
	}
	return q
}

// Where each kind of query is answered (#151).
func TestAnswerGoesWhereTheQueryAsks(t *testing.T) {
	svc := testService(t)
	full := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 9), Port: mdnsPort}
	legacy := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 9), Port: 49152}

	q := ptrQuery(false)
	q.Id = 7
	m, u := answer(svc, q, full)
	if m == nil || u != nil {
		t.Fatalf("query from 5353 without the unicast bit: multicast=%v unicast=%v, want the group only", m != nil, u != nil)
	}
	if m.Id != 0 {
		t.Errorf("multicast answer id = %d, want 0", m.Id)
	}

	q = ptrQuery(true)
	q.Id = 7
	m, u = answer(svc, q, full)
	if m != nil || u == nil {
		t.Fatalf("query with the unicast bit: multicast=%v unicast=%v, want unicast only", m != nil, u != nil)
	}
	if u.Id != 7 {
		t.Errorf("unicast answer id = %d, want the query's 7", u.Id)
	}

	q = ptrQuery(false)
	q.Id = 9
	m, u = answer(svc, q, legacy)
	if m != nil || u == nil {
		t.Fatalf("legacy query: multicast=%v unicast=%v, want unicast only", m != nil, u != nil)
	}
	if u.Id != 9 || len(u.Question) != 1 {
		t.Errorf("legacy answer id=%d questions=%d, want the query's id and question echoed", u.Id, len(u.Question))
	}

	// Our own answer comes back through loopback and must not be answered.
	if m, u := answer(svc, &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Question: q.Question}, full); m != nil || u != nil {
		t.Error("a response was answered")
	}
}

// The acceptance case of #151: a querier bound to 5353 on this PC, beside the system
// responder (Bonjour on macOS, Avahi where installed), asks the way a full mDNS
// participant does and hears the answer on the group. Needs a network that carries
// multicast, so it runs where RFM_TEST_MULTICAST is set, like the resolution test.
func TestAQuerierOn5353BesideTheSystemResponderHearsTheAnswer(t *testing.T) {
	if os.Getenv("RFM_TEST_MULTICAST") == "" {
		t.Skip("set RFM_TEST_MULTICAST on a machine whose network carries multicast")
	}
	key := testKey(t)
	ad := newAdvertiser(7433, discard)
	if err := ad.advertise(&state{Name: "pc1"}, key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ad.close)
	ours := key.deviceID()[:instanceIDLen]

	// The same bind the agent uses, so it shares the port with whatever already holds it.
	c, err := net.ListenMulticastUDP("udp4", multicastInterface(), mdnsGroup4)
	if err != nil {
		t.Fatalf("binding 5353 beside the system responder: %v", err)
	}
	defer c.Close()
	p := ipv4.NewPacketConn(c)
	if err := p.SetMulticastLoopback(true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetControlMessage(ipv4.FlagDst, true); err != nil {
		t.Fatal(err)
	}
	buf, _ := ptrQuery(false).Pack()
	if _, err := c.WriteToUDP(buf, mdnsGroup4); err != nil {
		t.Fatal(err)
	}
	if got := readAnswer(t, p, ours); got == nil || !got.Equal(mdnsGroup4.IP) {
		t.Errorf("answer sent to %v, want %v", got, mdnsGroup4.IP)
	}

	// A legacy query, the way the desktop client and `agent discover` ask, is still
	// answered by unicast to the port it came from.
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.WriteToUDP(buf, mdnsGroup4); err != nil {
		t.Fatal(err)
	}
	readAnswer(t, ipv4.NewPacketConn(l), ours)
}

// readAnswer waits for an answer naming our instance and returns the address it was sent
// to, or nil if the address is not known.
func readAnswer(t *testing.T, p *ipv4.PacketConn, ours string) net.IP {
	t.Helper()
	_ = p.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65536)
	for {
		n, cm, _, err := p.ReadFrom(buf)
		if err != nil {
			t.Fatalf("no answer naming %s: %v", ours, err)
		}
		var m dns.Msg
		if m.Unpack(buf[:n]) != nil || !m.Response {
			continue
		}
		for _, rr := range m.Answer {
			if ptr, ok := rr.(*dns.PTR); ok && strings.Contains(ptr.Ptr, ours) {
				if cm == nil {
					return nil
				}
				return cm.Dst
			}
		}
	}
}
