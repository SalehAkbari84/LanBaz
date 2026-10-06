package portmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// UPnP IGD: an SSDP search on the LAN, the router's device description, then
// SOAP calls to its WANIPConnection (or WANPPPConnection) service.

var serviceTypes = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

type upnp struct {
	local, gateway netip.Addr
	control        string // absolute control URL
	service        string // service type
	http           *http.Client
}

func (c *upnp) Method() string { return "UPnP" }

// ssdpSearch returns the LOCATION URLs of internet gateway devices. A variable
// so tests can skip the multicast search.
var ssdpSearch = func(ctx context.Context, local netip.Addr) ([]string, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: local.AsSlice()})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	to := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for _, st := range []string{"urn:schemas-upnp-org:device:InternetGatewayDevice:1", "urn:schemas-upnp-org:device:InternetGatewayDevice:2"} {
		msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + st + "\r\n\r\n"
		if _, err := conn.WriteToUDP([]byte(msg), to); err != nil {
			return nil, err
		}
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	var out []string
	seen := map[string]bool{}
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(buf[:n])), nil)
		if err != nil {
			continue
		}
		loc := resp.Header.Get("Location")
		_ = resp.Body.Close()
		if loc != "" && !seen[loc] {
			seen[loc] = true
			out = append(out, loc)
		}
	}
	return out, nil
}

// onLAN accepts only a description served by the gateway itself or another
// device on this PC's own /24: an answer must not point us anywhere else.
var onLAN = func(host string, local, gateway netip.Addr) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if ip == gateway {
		return true
	}
	p, _ := local.Prefix(24)
	return ip.IsPrivate() && p.Contains(ip)
}

type device struct {
	URLBase string  `xml:"URLBase"`
	Device  devTree `xml:"device"`
}

type devTree struct {
	Services []service `xml:"serviceList>service"`
	Devices  []devTree `xml:"deviceList>device"`
}

type service struct {
	Type    string `xml:"serviceType"`
	Control string `xml:"controlURL"`
}

func findService(d devTree) (service, bool) {
	for _, want := range serviceTypes {
		for _, s := range d.Services {
			if s.Type == want {
				return s, true
			}
		}
	}
	for _, sub := range d.Devices {
		if s, ok := findService(sub); ok {
			return s, true
		}
	}
	return service{}, false
}

func discoverUPnP(ctx context.Context, local, gateway netip.Addr) (*upnp, error) {
	locs, err := ssdpSearch(ctx, local)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 4 * time.Second}
	for _, loc := range locs {
		u, err := url.Parse(loc)
		if err != nil || u.Scheme != "http" || !onLAN(u.Hostname(), local, gateway) {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		_ = resp.Body.Close()
		var d device
		if xml.Unmarshal(body, &d) != nil {
			continue
		}
		s, ok := findService(d.Device)
		if !ok {
			continue
		}
		base := u
		if d.URLBase != "" {
			if b, err := url.Parse(d.URLBase); err == nil {
				base = b
			}
		}
		ctl, err := base.Parse(s.Control)
		if err != nil || !onLAN(ctl.Hostname(), local, gateway) {
			continue
		}
		return &upnp{local: local, gateway: gateway, control: ctl.String(), service: s.Type, http: hc}, nil
	}
	return nil, errors.New("UPnP: no internet gateway device answered")
}

type soapFault struct {
	Code        int    `xml:"Body>Fault>detail>UPnPError>errorCode"`
	Description string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
}

func (c *upnp) soap(ctx context.Context, action string, args [][2]string) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	fmt.Fprintf(&b, `<u:%s xmlns:u="%s">`, action, c.service)
	for _, a := range args {
		fmt.Fprintf(&b, "<%s>", a[0])
		_ = xml.EscapeText(&b, []byte(a[1]))
		fmt.Fprintf(&b, "</%s>", a[0])
	}
	fmt.Fprintf(&b, `</u:%s></s:Body></s:Envelope>`, action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.control, strings.NewReader(b.String()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, c.service, action))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var f soapFault
		_ = xml.Unmarshal(body, &f)
		return nil, &upnpError{code: f.Code, desc: f.Description, status: resp.StatusCode}
	}
	return body, nil
}

type upnpError struct {
	code   int
	desc   string
	status int
}

func (e *upnpError) Error() string {
	if e.code != 0 {
		return fmt.Sprintf("UPnP: %d %s", e.code, e.desc)
	}
	return fmt.Sprintf("UPnP: HTTP %d", e.status)
}

func (c *upnp) ExternalIP(ctx context.Context) (netip.Addr, error) {
	body, err := c.soap(ctx, "GetExternalIPAddress", nil)
	if err != nil {
		return netip.Addr{}, err
	}
	var r struct {
		IP string `xml:"Body>GetExternalIPAddressResponse>NewExternalIPAddress"`
	}
	if err := xml.Unmarshal(body, &r); err != nil {
		return netip.Addr{}, err
	}
	return netip.ParseAddr(strings.TrimSpace(r.IP))
}

func (c *upnp) Map(ctx context.Context, internal uint16, lifetime time.Duration) (Mapping, error) {
	ext, err := c.ExternalIP(ctx)
	if err != nil {
		return Mapping{}, err
	}
	add := func(external uint16, lease time.Duration) error {
		_, err := c.soap(ctx, "AddPortMapping", [][2]string{
			{"NewRemoteHost", ""},
			{"NewExternalPort", strconv.Itoa(int(external))},
			{"NewProtocol", "UDP"},
			{"NewInternalPort", strconv.Itoa(int(internal))},
			{"NewInternalClient", c.local.String()},
			{"NewEnabled", "1"},
			{"NewPortMappingDescription", "LanBaz"},
			{"NewLeaseDuration", strconv.Itoa(int(lease / time.Second))},
		})
		return err
	}
	external, lease := internal, lifetime
	err = add(external, lease)
	var ue *upnpError
	if errors.As(err, &ue) && ue.code == 725 { // OnlyPermanentLeasesSupported
		lease = 0
		err = add(external, lease)
	}
	if errors.As(err, &ue) && ue.code == 718 { // ConflictInMappingEntry: someone else has it
		external = internal + 1000
		err = add(external, lease)
	}
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{Method: c.Method(), Gateway: c.gateway, ExternalIP: ext, Internal: internal, External: external, Lifetime: lease}, nil
}

func (c *upnp) Unmap(ctx context.Context, m Mapping) error {
	_, err := c.soap(ctx, "DeletePortMapping", [][2]string{
		{"NewRemoteHost", ""},
		{"NewExternalPort", strconv.Itoa(int(m.External))},
		{"NewProtocol", "UDP"},
	})
	return err
}
