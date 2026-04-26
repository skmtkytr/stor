// Port mapping for incoming peer connections.
//
// Tries UPnP-IGD first (most consumer routers support it), falls back to
// NAT-PMP (RFC 6886) on failure. Holds the mapping with a periodic lease
// refresh, and removes it on Stop. Implementation uses only the Go
// standard library: net for UDP/TCP, net/http for SOAP, encoding/xml for
// device description and SOAP body parsing.
//
// Without an external port mapping, peers behind NAT can only make
// outgoing connections; the most active peers (often those with capacity
// to share) often refuse to connect outbound, halving discovery rate.

package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	portMapDescription = "stor"
	portMapLease       = 3600 * time.Second // 1 hour, renew at half
	ssdpAddr           = "239.255.255.250:1900"
	ssdpST             = "urn:schemas-upnp-org:device:InternetGatewayDevice:1"
	natpmpPort         = 5351
)

// PortMapper installs and refreshes a port mapping on the local NAT
// gateway. Safe to use a nil receiver (all methods become no-ops).
type PortMapper struct {
	internal uint16

	mu       sync.Mutex
	external uint16
	method   string                // "upnp" / "natpmp" / ""
	teardown func(context.Context) // best-effort unmap
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewPortMapper returns a mapper that will request external == internal
// for the given local listening port.
func NewPortMapper(internal uint16) *PortMapper {
	return &PortMapper{internal: internal}
}

// ExternalPort returns the externally-visible port if a mapping is
// active, otherwise 0.
func (m *PortMapper) ExternalPort() uint16 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.external
}

// Method returns "upnp" / "natpmp" / "" depending on which protocol is
// holding the mapping (empty when no mapping is active).
func (m *PortMapper) Method() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.method
}

// Start runs discovery and lease refresh in a goroutine. It returns
// immediately; check Method/ExternalPort to see whether a mapping was
// installed. The mapping persists until Stop is called or ctx is
// cancelled.
func (m *PortMapper) Start(ctx context.Context) {
	if m == nil || m.internal == 0 {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	m.wg.Add(1)
	go m.run(ctx)
}

// Stop tears down the active mapping (best-effort, short timeout) and
// stops the refresh loop.
func (m *PortMapper) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.cancel
	teardown := m.teardown
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if teardown != nil {
		tctx, c := context.WithTimeout(context.Background(), 3*time.Second)
		teardown(tctx)
		c()
	}
	m.wg.Wait()
}

func (m *PortMapper) run(ctx context.Context) {
	defer m.wg.Done()

	// Discovery + initial mapping.
	if ok := m.tryUPnP(ctx); !ok {
		if ok := m.tryNATPMP(ctx); !ok {
			slog.Info("portmap: neither UPnP nor NAT-PMP succeeded; relying on outbound connections only")
			return
		}
	}

	// Renew at half the lease so we have a generous overlap.
	refresh := portMapLease / 2
	timer := time.NewTimer(refresh)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.mu.Lock()
			method := m.method
			m.mu.Unlock()
			switch method {
			case "upnp":
				if !m.tryUPnP(ctx) {
					slog.Warn("portmap: UPnP refresh failed; falling back to NAT-PMP")
					if !m.tryNATPMP(ctx) {
						slog.Warn("portmap: NAT-PMP fallback also failed; mapping likely lost until next discovery")
					}
				}
			case "natpmp":
				if !m.tryNATPMP(ctx) {
					slog.Warn("portmap: NAT-PMP refresh failed")
				}
			}
			timer.Reset(refresh)
		}
	}
}

// --- UPnP / SSDP ---------------------------------------------------------

// ssdpResponse is one IGD response to an M-SEARCH multicast.
type ssdpResponse struct {
	location string // URL of device description XML
	source   net.IP // IP that sent the response (likely the gateway)
}

// ssdpDiscover sends an M-SEARCH multicast and collects responses for
// up to the given duration.
func ssdpDiscover(ctx context.Context, timeout time.Duration) ([]ssdpResponse, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, fmt.Errorf("ssdp: listen: %w", err)
	}
	defer conn.Close()

	addr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return nil, fmt.Errorf("ssdp: resolve: %w", err)
	}

	req := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + ssdpST + "\r\n\r\n"

	// Send a couple of times to defeat occasional UDP loss.
	for i := 0; i < 2; i++ {
		if _, err := conn.WriteTo([]byte(req), addr); err != nil {
			return nil, fmt.Errorf("ssdp: write: %w", err)
		}
	}

	deadline := time.Now().Add(timeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("ssdp: set deadline: %w", err)
	}

	var out []ssdpResponse
	seen := make(map[string]bool)
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			break
		}
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			break // deadline or closed
		}
		loc := parseSSDPLocation(buf[:n])
		if loc == "" || seen[loc] {
			continue
		}
		seen[loc] = true
		ip, _ := src.(*net.UDPAddr)
		var ipv4 net.IP
		if ip != nil {
			ipv4 = ip.IP
		}
		out = append(out, ssdpResponse{location: loc, source: ipv4})
	}
	return out, nil
}

func parseSSDPLocation(data []byte) string {
	for _, line := range bytes.Split(data, []byte("\r\n")) {
		if len(line) < 9 {
			continue
		}
		if !bytes.EqualFold(line[:9], []byte("LOCATION:")) {
			continue
		}
		return strings.TrimSpace(string(line[9:]))
	}
	return ""
}

// upnpDevice / upnpService / etc. mirror the subset of the IGD device
// description we care about.
type upnpDeviceXML struct {
	XMLName xml.Name   `xml:"root"`
	Device  upnpDevice `xml:"device"`
	URLBase string     `xml:"URLBase"`
}

type upnpDevice struct {
	DeviceList struct {
		Device []upnpDevice `xml:"device"`
	} `xml:"deviceList"`
	ServiceList struct {
		Service []upnpService `xml:"service"`
	} `xml:"serviceList"`
}

type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// findUPnPControlURL walks the device tree looking for a
// WAN{IP,PPP}Connection service and returns its absolute control URL.
func findUPnPControlURL(deviceURL string, body []byte) (controlURL, serviceType string, err error) {
	var root upnpDeviceXML
	if err := xml.Unmarshal(body, &root); err != nil {
		return "", "", fmt.Errorf("upnp: parse device xml: %w", err)
	}

	base := root.URLBase
	if base == "" {
		base = deviceURL
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", "", fmt.Errorf("upnp: parse URLBase: %w", err)
	}

	var walk func(d upnpDevice) (string, string)
	walk = func(d upnpDevice) (string, string) {
		for _, s := range d.ServiceList.Service {
			if s.ServiceType == "urn:schemas-upnp-org:service:WANIPConnection:1" ||
				s.ServiceType == "urn:schemas-upnp-org:service:WANIPConnection:2" ||
				s.ServiceType == "urn:schemas-upnp-org:service:WANPPPConnection:1" {
				cu, err := url.Parse(s.ControlURL)
				if err != nil {
					continue
				}
				return baseURL.ResolveReference(cu).String(), s.ServiceType
			}
		}
		for _, child := range d.DeviceList.Device {
			if cu, st := walk(child); cu != "" {
				return cu, st
			}
		}
		return "", ""
	}
	cu, st := walk(root.Device)
	if cu == "" {
		return "", "", errors.New("upnp: no WANConnection service found")
	}
	return cu, st, nil
}

// localIPTowards returns the local IP the OS would use to reach `target`.
// We use this to learn our address as seen by the gateway, which UPnP
// requires in NewInternalClient.
func localIPTowards(target string) (net.IP, error) {
	conn, err := net.Dial("udp4", target)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return nil, errors.New("local addr unavailable")
	}
	return addr.IP, nil
}

// soapAddPortMapping issues both TCP and UDP AddPortMapping calls.
// Returns the external port granted by the IGD (we always request the
// same port as internal; some IGDs honour it, others substitute).
func soapAddPortMapping(ctx context.Context, controlURL, serviceType string, internal uint16, internalIP net.IP) (uint16, error) {
	// Most IGDs use external == internal when the request matches. We
	// don't try to negotiate a different external port if the requested
	// one is taken; falling back is rare and the user can change
	// ListenPort manually.
	external := internal
	for _, proto := range []string{"TCP", "UDP"} {
		body := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:AddPortMapping xmlns:u="%s">
<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>%s</NewProtocol>
<NewInternalPort>%d</NewInternalPort>
<NewInternalClient>%s</NewInternalClient>
<NewEnabled>1</NewEnabled>
<NewPortMappingDescription>%s</NewPortMappingDescription>
<NewLeaseDuration>%d</NewLeaseDuration>
</u:AddPortMapping>
</s:Body>
</s:Envelope>`, serviceType, external, proto, internal, internalIP.String(), portMapDescription, int(portMapLease/time.Second))

		if err := soapCall(ctx, controlURL, serviceType, "AddPortMapping", body); err != nil {
			return 0, fmt.Errorf("upnp: AddPortMapping(%s): %w", proto, err)
		}
	}
	return external, nil
}

// soapDeletePortMapping removes both TCP and UDP mappings created earlier.
func soapDeletePortMapping(ctx context.Context, controlURL, serviceType string, external uint16) error {
	var firstErr error
	for _, proto := range []string{"TCP", "UDP"} {
		body := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:DeletePortMapping xmlns:u="%s">
<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>%s</NewProtocol>
</u:DeletePortMapping>
</s:Body>
</s:Envelope>`, serviceType, external, proto)
		if err := soapCall(ctx, controlURL, serviceType, "DeletePortMapping", body); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// soapCall sends a SOAP request to controlURL and reports a non-nil error
// when the IGD returns a SOAP fault or non-2xx status.
func soapCall(ctx context.Context, controlURL, serviceType, action, body string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", controlURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, serviceType, action))
	req.Header.Set("Connection", "close")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("soap %s: HTTP %d: %s", action, resp.StatusCode, snippet(respBody))
	}
	return nil
}

func snippet(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

// tryUPnP runs SSDP + SOAP. Returns true on success and stores teardown
// state on m so Stop() can later DeletePortMapping.
func (m *PortMapper) tryUPnP(ctx context.Context) bool {
	dctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	devs, err := ssdpDiscover(dctx, 4*time.Second)
	cancel()
	if err != nil {
		slog.Debug("portmap: SSDP discover failed", "error", err)
		return false
	}
	for _, dev := range devs {
		body, err := fetchURL(ctx, dev.location, 5*time.Second)
		if err != nil {
			slog.Debug("portmap: fetch device xml failed", "url", dev.location, "error", err)
			continue
		}
		controlURL, serviceType, err := findUPnPControlURL(dev.location, body)
		if err != nil {
			continue
		}
		// Determine our local IP as seen by the gateway. Prefer the SSDP
		// source IP because it is guaranteed reachable; fall back to a
		// generic public-route probe if SSDP source was empty.
		var target string
		if dev.source != nil {
			target = net.JoinHostPort(dev.source.String(), "9")
		} else {
			target = "8.8.8.8:80"
		}
		localIP, err := localIPTowards(target)
		if err != nil {
			continue
		}
		external, err := soapAddPortMapping(ctx, controlURL, serviceType, m.internal, localIP)
		if err != nil {
			slog.Debug("portmap: AddPortMapping failed", "url", controlURL, "error", err)
			continue
		}
		m.mu.Lock()
		m.external = external
		m.method = "upnp"
		m.teardown = func(tctx context.Context) {
			if err := soapDeletePortMapping(tctx, controlURL, serviceType, external); err != nil {
				slog.Debug("portmap: DeletePortMapping failed", "error", err)
			}
		}
		m.mu.Unlock()
		slog.Info("portmap: UPnP mapping installed", "internal", m.internal, "external", external, "gateway_url", controlURL)
		return true
	}
	return false
}

func fetchURL(ctx context.Context, u string, timeout time.Duration) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<10))
}

// --- NAT-PMP (RFC 6886) --------------------------------------------------

// natpmpMap sends one map request to gateway:5351 for the given protocol
// and returns the granted external port and lease seconds.
//
// Wire format:
//
//	Request (12 bytes):
//	  byte 0:    version = 0
//	  byte 1:    op (1=UDP, 2=TCP)
//	  bytes 2-3: reserved (0)
//	  bytes 4-5: internal port (uint16 BE)
//	  bytes 6-7: external port suggestion (uint16 BE; 0 = any)
//	  bytes 8-11: lifetime seconds (uint32 BE)
//
//	Response (16 bytes):
//	  byte 0:     version
//	  byte 1:     op | 0x80
//	  bytes 2-3:  result code
//	  bytes 4-7:  seconds since gateway epoch
//	  bytes 8-9:  internal port
//	  bytes 10-11: external port assigned
//	  bytes 12-15: lifetime granted
func natpmpMap(ctx context.Context, gateway net.IP, op byte, internal uint16, lifetime uint32) (external uint16, granted uint32, err error) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: gateway, Port: natpmpPort})
	if err != nil {
		return 0, 0, fmt.Errorf("natpmp: dial: %w", err)
	}
	defer conn.Close()

	req := make([]byte, 12)
	req[0] = 0
	req[1] = op
	binary.BigEndian.PutUint16(req[4:6], internal)
	binary.BigEndian.PutUint16(req[6:8], internal)
	binary.BigEndian.PutUint32(req[8:12], lifetime)

	// RFC 6886 retry: 250ms, 500ms, 1000ms, 2000ms — up to ~3.75s total.
	resp := make([]byte, 16)
	delays := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, 1000 * time.Millisecond, 2000 * time.Millisecond}
	for _, d := range delays {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		if _, err := conn.Write(req); err != nil {
			return 0, 0, fmt.Errorf("natpmp: write: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(d))
		n, err := conn.Read(resp)
		if err == nil && n >= 16 {
			result := binary.BigEndian.Uint16(resp[2:4])
			if result != 0 {
				return 0, 0, fmt.Errorf("natpmp: gateway returned result=%d", result)
			}
			external = binary.BigEndian.Uint16(resp[10:12])
			granted = binary.BigEndian.Uint32(resp[12:16])
			return external, granted, nil
		}
	}
	return 0, 0, errors.New("natpmp: no response from gateway")
}

// guessGateway returns the most likely default gateway IPv4 address. We
// derive the local /24 by routing toward 8.8.8.8 and use .1 as the
// gateway. This covers the common home-router topology; users on other
// topologies will fall back to outbound-only peers.
func guessGateway() (net.IP, error) {
	local, err := localIPTowards("8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	v4 := local.To4()
	if v4 == nil {
		return nil, errors.New("non-IPv4 local address")
	}
	gw := append(net.IP(nil), v4...)
	gw[3] = 1
	return gw, nil
}

func (m *PortMapper) tryNATPMP(ctx context.Context) bool {
	gw, err := guessGateway()
	if err != nil {
		slog.Debug("portmap: gateway guess failed", "error", err)
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	lifetime := uint32(portMapLease / time.Second)
	// Map both TCP (op=2) and UDP (op=1).
	extTCP, _, err := natpmpMap(cctx, gw, 2, m.internal, lifetime)
	if err != nil {
		slog.Debug("portmap: NAT-PMP TCP map failed", "gateway", gw, "error", err)
		return false
	}
	extUDP, _, err := natpmpMap(cctx, gw, 1, m.internal, lifetime)
	if err != nil {
		slog.Debug("portmap: NAT-PMP UDP map failed", "gateway", gw, "error", err)
		// Best-effort unmap of the TCP we just installed.
		_, _, _ = natpmpMap(context.Background(), gw, 2, m.internal, 0)
		return false
	}
	if extTCP != extUDP {
		slog.Debug("portmap: NAT-PMP TCP/UDP external ports differ", "tcp", extTCP, "udp", extUDP)
	}

	m.mu.Lock()
	m.external = extTCP
	m.method = "natpmp"
	m.teardown = func(tctx context.Context) {
		// lifetime=0 unmaps per RFC 6886.
		_, _, _ = natpmpMap(tctx, gw, 1, m.internal, 0)
		_, _, _ = natpmpMap(tctx, gw, 2, m.internal, 0)
	}
	m.mu.Unlock()
	slog.Info("portmap: NAT-PMP mapping installed", "internal", m.internal, "external", extTCP, "gateway", gw)
	return true
}
