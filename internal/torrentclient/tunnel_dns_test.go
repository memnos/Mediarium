package torrentclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootstrapUsesTheTunnelAndNotTheHost(t *testing.T) {
	var host atomic.Int32
	prev := noteHostDNS
	noteHostDNS = func() { host.Add(1) }
	t.Cleanup(func() { noteHostDNS = prev })

	names := lookupTunnel{fn: func(_ context.Context, host string) ([]net.IP, error) {
		if host == "router.bittorrent.com" {
			return []net.IP{net.ParseIP("192.0.2.8")}, nil
		}
		return nil, errors.New("no address")
	}}
	addrs, err := bootstrapThroughTunnel("udp4", names)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(addrs) != 1 || addrs[0].IP().String() != "192.0.2.8" || addrs[0].Port() != 6881 {
		t.Fatalf("bootstrap addresses = %v", addrs)
	}
	if host.Load() != 0 {
		t.Fatalf("host DNS was used %d times", host.Load())
	}
}

func TestBootstrapFailsClosed(t *testing.T) {
	var host atomic.Int32
	prev := noteHostDNS
	noteHostDNS = func() { host.Add(1) }
	t.Cleanup(func() { noteHostDNS = prev })

	_, err := bootstrapThroughTunnel("udp4", lookupTunnel{fn: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("vpn dns refused")
	}})
	if err == nil {
		t.Fatal("bootstrap succeeded with no names")
	}
	if host.Load() != 0 {
		t.Fatalf("a failed bootstrap used host DNS %d times", host.Load())
	}
}

func TestTrackerNamesResolveThroughTheTunnel(t *testing.T) {
	var looked sync.Map
	var host atomic.Int32
	prev := noteHostDNS
	noteHostDNS = func() { host.Add(1) }
	t.Cleanup(func() { noteHostDNS = prev })

	tun := &nameTunnel{lookup: func(_ context.Context, host string) ([]net.IP, error) {
		looked.Store(host, true)
		if host == "tracker.example" {
			return nil, errors.New("vpn dns refused")
		}
		return nil, errors.New("no address")
	}}
	engine, err := New(Config{DataDir: t.TempDir(), Tunnel: tun, DiscoverThroughTunnel: true})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	defer engine.Close()

	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=fixture&tr=udp%3A%2F%2Ftracker.example%3A6969%2Fannounce"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tor, err := engine.AddMagnetIn(ctx, magnet, t.TempDir())
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("add magnet: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := looked.Load("tracker.example"); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := looked.Load("tracker.example"); !ok {
		t.Fatalf("the tracker name was not looked up through the tunnel (host DNS %d)", host.Load())
	}
	if host.Load() != 0 {
		t.Fatalf("the tracker name was also looked up on the host (%d times)", host.Load())
	}
	if tor != nil {
		engine.Remove(tor)
	}
}

func TestTunnelSocketFailureSendsNothingFromTheHost(t *testing.T) {
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.LocalAddr().(*net.UDPAddr).Port

	tun := &failingPacketTunnel{}
	engine, err := New(Config{DataDir: t.TempDir(), Tunnel: tun, DiscoverThroughTunnel: true})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	defer engine.Close()
	if tun.opens.Load() < 1 {
		t.Fatal("the tunnel socket was never asked for")
	}

	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=fixture&tr=udp%3A%2F%2F127.0.0.1%3A" + itoa(port) + "%2Fannounce"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tor, err := engine.AddMagnetIn(ctx, magnet, t.TempDir())
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("add magnet: %v", err)
	}
	_ = ln.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 2048)
	n, addr, rerr := ln.ReadFrom(buf)
	if rerr == nil {
		t.Fatalf("the host received %d bytes from %s after the tunnel socket failed", n, addr)
	}
	if tor != nil {
		engine.Remove(tor)
	}
}

func TestTunnelDNSAnswer(t *testing.T) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return &tunnelDNSConn{ctx: ctx, lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("192.0.2.9")}, nil
		}}, nil
	}}
	ips, err := r.LookupIP(context.Background(), "ip4", "tracker.example")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(ips) != 1 || ips[0].String() != "192.0.2.9" {
		t.Fatalf("addresses = %v", ips)
	}
}

func TestTunnelDNSFailureDoesNotInventAnAddress(t *testing.T) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return &tunnelDNSConn{ctx: ctx, lookup: func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("vpn dns refused")
		}}, nil
	}}
	if _, err := r.LookupIP(context.Background(), "ip4", "tracker.example"); err == nil {
		t.Fatal("a failed VPN lookup returned an address")
	}
}

type lookupTunnel struct {
	fn func(context.Context, string) ([]net.IP, error)
}

func (l lookupTunnel) LookupHost(ctx context.Context, host string) ([]net.IP, error) {
	return l.fn(ctx, host)
}

// nameTunnel carries UDP and resolves names. Writes are recorded and never
// leave the test.
type nameTunnel struct {
	refusingDial
	lookup func(context.Context, string) ([]net.IP, error)
	wrote  atomic.Pointer[net.UDPAddr]
}

func (n *nameTunnel) ListenPacket(string, string) (net.PacketConn, error) {
	return &recordingPacket{parent: n, done: make(chan struct{})}, nil
}

func (n *nameTunnel) LookupHost(ctx context.Context, host string) ([]net.IP, error) {
	return n.lookup(ctx, host)
}

type recordingPacket struct {
	parent *nameTunnel
	done   chan struct{}
	once   sync.Once
}

func (p *recordingPacket) ReadFrom([]byte) (int, net.Addr, error) {
	<-p.done
	return 0, nil, net.ErrClosed
}
func (p *recordingPacket) WriteTo(b []byte, addr net.Addr) (int, error) {
	if ua, ok := addr.(*net.UDPAddr); ok {
		p.parent.wrote.Store(ua)
	}
	return len(b), nil
}
func (p *recordingPacket) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *recordingPacket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(10, 2, 0, 2), Port: 1}
}
func (p *recordingPacket) SetDeadline(time.Time) error      { return nil }
func (p *recordingPacket) SetReadDeadline(time.Time) error  { return nil }
func (p *recordingPacket) SetWriteDeadline(time.Time) error { return nil }

// failingPacketTunnel opens the first socket (DHT) and refuses every later
// one, which is what a tracker announce asks for.
type failingPacketTunnel struct {
	refusingDial
	opens atomic.Int32
}

func (f *failingPacketTunnel) ListenPacket(string, string) (net.PacketConn, error) {
	if f.opens.Add(1) == 1 {
		return &heldPacket{done: make(chan struct{})}, nil
	}
	return nil, errors.New("tunnel socket refused")
}

func (f *failingPacketTunnel) LookupHost(context.Context, string) ([]net.IP, error) {
	return nil, errors.New("no dns in this test")
}

type refusingDial struct{}

func (refusingDial) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("tunnel test: no connection")
}

func itoa(n int) string { return strconv.Itoa(n) }

// heldPacket is a tunnel socket that never sends anywhere.
type heldPacket struct {
	done chan struct{}
	once sync.Once
}

func (p *heldPacket) ReadFrom([]byte) (int, net.Addr, error) {
	<-p.done
	return 0, nil, net.ErrClosed
}
func (p *heldPacket) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }
func (p *heldPacket) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *heldPacket) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4(10, 2, 0, 2), Port: 1} }
func (p *heldPacket) SetDeadline(time.Time) error      { return nil }
func (p *heldPacket) SetReadDeadline(time.Time) error  { return nil }
func (p *heldPacket) SetWriteDeadline(time.Time) error { return nil }
