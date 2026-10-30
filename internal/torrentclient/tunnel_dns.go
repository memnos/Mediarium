package torrentclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/dht/v2"
	"golang.org/x/net/dns/dnsmessage"
)

// The UDP tracker client inside the engine resolves a tracker hostname with
// net.ResolveUDPAddr, which asks the host's DNS, and that call has no hook.
// While discovery through the VPN is on, the process resolver answers those
// lookups (and any DHT lookup that still reaches it) from the tunnel. Every
// other lookup is dialled the way it was before. A failed tunnel lookup is
// returned as a failure: it is never tried again on the home connection.

type hostLookup func(context.Context, string) ([]net.IP, error)

var (
	dnsMu     sync.Mutex
	dnsUsers  int
	dnsSaved  resolverSnapshot
	dnsLookup atomic.Value // hostLookup

	// noteHostDNS runs when a lookup is about to use the host resolver.
	// Tests replace it to prove a tracker name did not take that path.
	noteHostDNS = func() {}
)

type resolverSnapshot struct {
	dial     func(context.Context, string, string) (net.Conn, error)
	preferGo bool
}

// installTunnelDNS sends tracker hostname lookups through lookup until the
// returned function is called. Calls nest: the host resolver is restored
// when the last one is released.
func installTunnelDNS(lookup hostLookup) func() {
	dnsMu.Lock()
	defer dnsMu.Unlock()
	if dnsUsers == 0 {
		dnsSaved = resolverSnapshot{dial: net.DefaultResolver.Dial, preferGo: net.DefaultResolver.PreferGo}
		net.DefaultResolver.PreferGo = true
		net.DefaultResolver.Dial = tunnelDNSDial
	}
	dnsUsers++
	dnsLookup.Store(lookup)
	var once sync.Once
	return func() {
		once.Do(func() {
			dnsMu.Lock()
			defer dnsMu.Unlock()
			dnsUsers--
			if dnsUsers == 0 {
				net.DefaultResolver.Dial = dnsSaved.dial
				net.DefaultResolver.PreferGo = dnsSaved.preferGo
				dnsLookup.Store(hostLookup(nil))
			}
		})
	}
}

func currentLookup() hostLookup {
	v := dnsLookup.Load()
	fn, _ := v.(hostLookup)
	return fn
}

func tunnelDNSDial(ctx context.Context, network, address string) (net.Conn, error) {
	if lookupFromTunnelUser() {
		lookup := currentLookup()
		if lookup == nil {
			return nil, errors.New("the VPN lookup is not available")
		}
		return &tunnelDNSConn{ctx: ctx, lookup: lookup}, nil
	}
	noteHostDNS()
	if dnsSaved.dial != nil {
		return dnsSaved.dial(ctx, network, address)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

// lookupFromTunnelUser reports whether a UDP tracker announce is waiting on
// this lookup. The engine resolves that hostname with net.ResolveUDPAddr and
// offers no resolver to pass in, and the Go resolver does the exchange on a
// fresh goroutine, so this goroutine's own callers are only the resolver.
// The announce is still on the stack of the goroutine that asked, which is
// how the lookup is told apart from every other one Mediarium makes. DHT
// bootstrap does not come through here: it calls the tunnel lookup itself.
func lookupFromTunnelUser() bool {
	buf := make([]byte, 64<<10)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= 1<<20 {
			return bytes.Contains(buf[:n], []byte("/torrent/tracker/udp."))
		}
		buf = make([]byte, len(buf)*2)
	}
}

// tunnelDNSConn answers one DNS query from the tunnel lookup. It never
// dials a DNS server on the host. It implements net.PacketConn so the Go
// resolver treats the reply as a UDP DNS message.
type tunnelDNSConn struct {
	ctx    context.Context
	lookup hostLookup
	mu     sync.Mutex
	buf    []byte
	err    error
}

func (c *tunnelDNSConn) Write(b []byte) (int, error) {
	resp, err := answerQuery(c.ctx, b, c.lookup)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.err = err
		return 0, err
	}
	c.buf = resp
	return len(b), nil
}

func (c *tunnelDNSConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	if len(c.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(b, c.buf)
	c.buf = nil
	return n, nil
}

func (c *tunnelDNSConn) Close() error                     { return nil }
func (c *tunnelDNSConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *tunnelDNSConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (c *tunnelDNSConn) SetDeadline(time.Time) error      { return nil }
func (c *tunnelDNSConn) SetReadDeadline(time.Time) error  { return nil }
func (c *tunnelDNSConn) SetWriteDeadline(time.Time) error { return nil }
func (c *tunnelDNSConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := c.Read(b)
	return n, &net.UDPAddr{}, err
}
func (c *tunnelDNSConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }

func answerQuery(ctx context.Context, query []byte, lookup hostLookup) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(q.Name.String(), ".")
	ips, err := lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 h.ID,
		Response:           true,
		Authoritative:      true,
		RecursionDesired:   h.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			if q.Type != dnsmessage.TypeA {
				continue
			}
			var a [4]byte
			copy(a[:], v4)
			hdr := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}
			if err := b.AResource(hdr, dnsmessage.AResource{A: a}); err != nil {
				return nil, err
			}
			continue
		}
		if q.Type != dnsmessage.TypeAAAA {
			continue
		}
		var a [16]byte
		copy(a[:], ip.To16())
		hdr := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: 30}
		if err := b.AAAAResource(hdr, dnsmessage.AAAAResource{AAAA: a}); err != nil {
			return nil, err
		}
	}
	return b.Finish()
}

// tunnelBootstrap resolves the public DHT routers through the tunnel.
// It never calls the host resolver. If none of the names resolve, the
// error stops bootstrap instead of trying the home connection.
func tunnelBootstrap(names TunnelNames) func(string) dht.StartingNodesGetter {
	return func(network string) dht.StartingNodesGetter {
		return func() ([]dht.Addr, error) {
			return bootstrapThroughTunnel(network, names)
		}
	}
}

func bootstrapThroughTunnel(network string, names TunnelNames) ([]dht.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var addrs []dht.Addr
	for _, hp := range dht.DefaultGlobalBootstrapHostPorts {
		host, portStr, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		ips, err := names.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			v4 := ip.To4() != nil
			if network == "udp4" && !v4 {
				continue
			}
			if network == "udp6" && v4 {
				continue
			}
			addrs = append(addrs, dht.NewAddr(&net.UDPAddr{IP: ip, Port: port}))
		}
	}
	if len(addrs) == 0 {
		return nil, errors.New("DHT bootstrap names could not be looked up through the VPN")
	}
	return addrs, nil
}
