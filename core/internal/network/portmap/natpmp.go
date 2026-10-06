package portmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// NAT-PMP, RFC 6886: two tiny UDP messages to the gateway on port 5351.

// natpmpPort is a variable so tests can run a fake router on loopback.
var natpmpPort uint16 = 5351

type natpmp struct {
	local, gateway netip.Addr
}

func newNATPMP(local, gateway netip.Addr) *natpmp { return &natpmp{local: local, gateway: gateway} }

func (c *natpmp) Method() string { return "NAT-PMP" }

var natpmpErrors = map[uint16]string{
	1: "unsupported version", 2: "refused (turned off in the router)", 3: "network failure",
	4: "out of resources", 5: "unsupported opcode",
}

// call sends req and waits for a reply with the expected opcode, retrying as
// the RFC asks (250 ms, doubling), three times.
func (c *natpmp) call(ctx context.Context, req []byte, wantOp byte, wantLen int) ([]byte, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: c.local.AsSlice()})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	to := &net.UDPAddr{IP: c.gateway.AsSlice(), Port: int(natpmpPort)}
	buf := make([]byte, 64)
	wait := 250 * time.Millisecond
	for try := 0; try < 3; try++ {
		if _, err := conn.WriteToUDP(req, to); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(wait)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetReadDeadline(deadline)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				break // timeout: retry
			}
			if !from.IP.Equal(c.gateway.AsSlice()) || n < wantLen || buf[0] != 0 || buf[1] != wantOp {
				continue
			}
			if code := binary.BigEndian.Uint16(buf[2:4]); code != 0 {
				msg := natpmpErrors[code]
				if msg == "" {
					msg = fmt.Sprintf("result %d", code)
				}
				return nil, fmt.Errorf("NAT-PMP: %s", msg)
			}
			return buf[:n], nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		wait *= 2
	}
	return nil, fmt.Errorf("NAT-PMP: the router at %s did not answer", c.gateway)
}

func (c *natpmp) probe(ctx context.Context) bool {
	_, err := c.ExternalIP(ctx)
	return err == nil
}

func (c *natpmp) ExternalIP(ctx context.Context) (netip.Addr, error) {
	resp, err := c.call(ctx, []byte{0, 0}, 128, 12)
	if err != nil {
		return netip.Addr{}, err
	}
	ip, _ := netip.AddrFromSlice(resp[8:12])
	return ip, nil
}

func (c *natpmp) mapReq(internal, external uint16, lifetime time.Duration) []byte {
	req := make([]byte, 12)
	req[1] = 1 // map UDP
	binary.BigEndian.PutUint16(req[4:], internal)
	binary.BigEndian.PutUint16(req[6:], external)
	binary.BigEndian.PutUint32(req[8:], uint32(lifetime/time.Second))
	return req
}

func (c *natpmp) Map(ctx context.Context, internal uint16, lifetime time.Duration) (Mapping, error) {
	ext, err := c.ExternalIP(ctx)
	if err != nil {
		return Mapping{}, err
	}
	resp, err := c.call(ctx, c.mapReq(internal, internal, lifetime), 129, 16)
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{
		Method: c.Method(), Gateway: c.gateway, ExternalIP: ext,
		Internal: binary.BigEndian.Uint16(resp[8:10]), External: binary.BigEndian.Uint16(resp[10:12]),
		Lifetime: time.Duration(binary.BigEndian.Uint32(resp[12:16])) * time.Second,
	}, nil
}

// Unmap is a map request with lifetime 0 and external port 0.
func (c *natpmp) Unmap(ctx context.Context, m Mapping) error {
	_, err := c.call(ctx, c.mapReq(m.Internal, 0, 0), 129, 16)
	return err
}
