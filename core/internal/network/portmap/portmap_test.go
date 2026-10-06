package portmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

var loop = netip.MustParseAddr("127.0.0.1")

// fakeNATPMP answers like a home router: external 85.185.78.173, mappings
// granted as asked.
func fakeNATPMP(t *testing.T) (unmapped chan uint16) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loop.AsSlice()})
	if err != nil {
		t.Fatal(err)
	}
	old := natpmpPort
	natpmpPort = uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	t.Cleanup(func() { natpmpPort = old; conn.Close() })
	unmapped = make(chan uint16, 4)
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			switch {
			case n == 2 && buf[1] == 0:
				resp := make([]byte, 12)
				resp[1] = 128
				copy(resp[8:], []byte{85, 185, 78, 173})
				_, _ = conn.WriteToUDP(resp, from)
			case n == 12 && buf[1] == 1:
				resp := make([]byte, 16)
				resp[1] = 129
				copy(resp[8:10], buf[4:6]) // internal
				copy(resp[10:12], buf[6:8])
				copy(resp[12:16], buf[8:12])
				if binary.BigEndian.Uint32(buf[8:12]) == 0 {
					unmapped <- binary.BigEndian.Uint16(buf[4:6])
				}
				_, _ = conn.WriteToUDP(resp, from)
			}
		}
	}()
	return unmapped
}

func TestNATPMPMapsAndUnmaps(t *testing.T) {
	unmapped := fakeNATPMP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Discover(ctx, loop, loop)
	if err != nil || c.Method() != "NAT-PMP" {
		t.Fatalf("discover: %v %v", c, err)
	}
	m, err := c.Map(ctx, 47210, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if m.ExternalIP.String() != "85.185.78.173" || m.External != 47210 || m.Internal != 47210 || m.Lifetime != time.Hour {
		t.Fatalf("mapping %+v", m)
	}
	if err := c.Unmap(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := <-unmapped; got != 47210 {
		t.Fatalf("unmapped %d", got)
	}
}

// fakeIGD serves a device description and the WANIPConnection SOAP actions.
func fakeIGD(t *testing.T, permanentOnly bool) (*httptest.Server, *sync.Map) {
	t.Helper()
	mappings := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
<deviceList><device><deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
<deviceList><device><deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
<controlURL>/ctl/IPConn</controlURL></service></serviceList></device></deviceList></device></deviceList></device></root>`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		action := strings.Trim(strings.SplitN(r.Header.Get("SOAPAction"), "#", 2)[1], `"`)
		switch action {
		case "GetExternalIPAddress":
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"><NewExternalIPAddress>85.185.78.173</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`)
		case "AddPortMapping":
			if permanentOnly && !strings.Contains(s, "<NewLeaseDuration>0</NewLeaseDuration>") {
				w.WriteHeader(500)
				fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>725</errorCode><errorDescription>OnlyPermanentLeasesSupported</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
				return
			}
			port := between(s, "<NewExternalPort>", "</NewExternalPort>")
			mappings.Store(port, between(s, "<NewInternalClient>", "</NewInternalClient>"))
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body/></s:Envelope>`)
		case "DeletePortMapping":
			mappings.Delete(between(s, "<NewExternalPort>", "</NewExternalPort>"))
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body/></s:Envelope>`)
		default:
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	old := ssdpSearch
	ssdpSearch = func(context.Context, netip.Addr) ([]string, error) { return []string{srv.URL + "/desc.xml"}, nil }
	t.Cleanup(func() { ssdpSearch = old })
	return srv, mappings
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return s
}

func TestUPnPMapsAndUnmaps(t *testing.T) {
	natpmpPort = 1 // nothing answers there: NAT-PMP fails, UPnP is used
	t.Cleanup(func() { natpmpPort = 5351 })
	for _, permanentOnly := range []bool{false, true} {
		_, mappings := fakeIGD(t, permanentOnly)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, err := Discover(ctx, loop, loop)
		if err != nil || c.Method() != "UPnP" {
			cancel()
			t.Fatalf("discover: %v", err)
		}
		m, err := c.Map(ctx, 47211, time.Hour)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if client, ok := mappings.Load("47211"); !ok || client != "127.0.0.1" || m.ExternalIP.String() != "85.185.78.173" {
			t.Fatalf("mapping %+v, router has %v", m, client)
		}
		if permanentOnly && m.Lifetime != 0 {
			t.Fatalf("a permanent-only router must get lease 0, got %v", m.Lifetime)
		}
		if err := c.Unmap(ctx, m); err != nil {
			t.Fatal(err)
		}
		if _, ok := mappings.Load("47211"); ok {
			t.Fatal("not unmapped")
		}
		cancel()
	}
}

// A description that points somewhere off the LAN is never followed.
func TestUPnPRefusesOffLANDevices(t *testing.T) {
	natpmpPort = 1
	t.Cleanup(func() { natpmpPort = 5351 })
	old := ssdpSearch
	ssdpSearch = func(context.Context, netip.Addr) ([]string, error) {
		return []string{"http://203.0.113.9/desc.xml", "https://192.168.1.1/x"}, nil
	}
	t.Cleanup(func() { ssdpSearch = old })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := Discover(ctx, netip.MustParseAddr("192.168.1.4"), netip.MustParseAddr("192.168.1.1")); err != ErrUnsupported {
		t.Fatalf("got %v", err)
	}
}

func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{"85.185.78.173": true, "192.168.1.1": false, "10.1.2.3": false, "100.72.0.1": false, "0.0.0.0": false} {
		if Public(netip.MustParseAddr(s)) != want {
			t.Errorf("Public(%s) != %v", s, want)
		}
	}
}
