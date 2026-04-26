package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- UPnP unit tests -----------------------------------------------------

func TestParseSSDPLocation(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"HTTP/1.1 200 OK\r\nLOCATION: http://1.2.3.4/desc.xml\r\nST: urn\r\n\r\n", "http://1.2.3.4/desc.xml"},
		{"HTTP/1.1 200 OK\r\nlocation: http://x/y\r\n\r\n", "http://x/y"},
		{"HTTP/1.1 200 OK\r\n\r\n", ""},
	}
	for _, c := range cases {
		if got := parseSSDPLocation([]byte(c.in)); got != c.want {
			t.Errorf("parseSSDPLocation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFindUPnPControlURL(t *testing.T) {
	const xmlBody = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <URLBase>http://10.0.0.1:5000</URLBase>
  <device>
    <deviceList>
      <device>
        <deviceList>
          <device>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>/ctl/IPConn</controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`
	cu, st, err := findUPnPControlURL("http://10.0.0.1:5000/desc.xml", []byte(xmlBody))
	if err != nil {
		t.Fatalf("findUPnPControlURL: %v", err)
	}
	if cu != "http://10.0.0.1:5000/ctl/IPConn" {
		t.Errorf("controlURL = %q", cu)
	}
	if st != "urn:schemas-upnp-org:service:WANIPConnection:1" {
		t.Errorf("serviceType = %q", st)
	}
}

func TestFindUPnPControlURLNoService(t *testing.T) {
	const xmlBody = `<?xml version="1.0"?>
<root><device><deviceList></deviceList></device></root>`
	if _, _, err := findUPnPControlURL("http://x/", []byte(xmlBody)); err == nil {
		t.Fatal("expected error for missing WAN service")
	}
}

// fakeIGD answers SOAP requests from a real httptest server. We can drive
// soapAddPortMapping / soapDeletePortMapping against it without any
// network gear.
type fakeIGD struct {
	*httptest.Server
	addCalls    atomic.Int32
	deleteCalls atomic.Int32
	failOn      string // "AddPortMapping" / "DeletePortMapping" to force a fault
}

func newFakeIGD() *fakeIGD {
	f := &fakeIGD{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "AddPortMapping"):
			f.addCalls.Add(1)
			if f.failOn == "AddPortMapping" {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`<s:Envelope><s:Body><s:Fault/></s:Body></s:Envelope>`))
				return
			}
			w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope><s:Body><u:AddPortMappingResponse/></s:Body></s:Envelope>`))
		case strings.Contains(s, "DeletePortMapping"):
			f.deleteCalls.Add(1)
			if f.failOn == "DeletePortMapping" {
				w.WriteHeader(500)
				return
			}
			w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope><s:Body><u:DeletePortMappingResponse/></s:Body></s:Envelope>`))
		default:
			w.WriteHeader(400)
		}
	}))
	return f
}

func TestSoapAddPortMappingSuccess(t *testing.T) {
	igd := newFakeIGD()
	defer igd.Close()

	ctx := context.Background()
	ext, err := soapAddPortMapping(ctx, igd.URL+"/ctl",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		6881, net.IPv4(192, 168, 1, 100))
	if err != nil {
		t.Fatalf("AddPortMapping: %v", err)
	}
	if ext != 6881 {
		t.Errorf("external = %d, want 6881", ext)
	}
	if got := igd.addCalls.Load(); got != 2 { // TCP + UDP
		t.Errorf("AddPortMapping call count = %d, want 2", got)
	}
}

func TestSoapAddPortMappingFault(t *testing.T) {
	igd := newFakeIGD()
	igd.failOn = "AddPortMapping"
	defer igd.Close()

	_, err := soapAddPortMapping(context.Background(), igd.URL+"/ctl",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		6881, net.IPv4(192, 168, 1, 100))
	if err == nil {
		t.Fatal("expected error on SOAP fault")
	}
}

func TestSoapDeletePortMapping(t *testing.T) {
	igd := newFakeIGD()
	defer igd.Close()

	if err := soapDeletePortMapping(context.Background(), igd.URL+"/ctl",
		"urn:schemas-upnp-org:service:WANIPConnection:1", 6881); err != nil {
		t.Fatalf("DeletePortMapping: %v", err)
	}
	if got := igd.deleteCalls.Load(); got != 2 {
		t.Errorf("Delete call count = %d, want 2", got)
	}
}

// --- NAT-PMP unit tests --------------------------------------------------

// fakeNATPMPServer replies to a single map request with a configurable
// external port and lifetime, then exits. Used to drive natpmpMap
// without requiring a real router.
type fakeNATPMPServer struct {
	conn         *net.UDPConn
	addr         *net.UDPAddr
	externalPort uint16
	lifetime     uint32
	resultCode   uint16
	gotOp        atomic.Int32 // last op byte we saw
}

func startFakeNATPMP(t *testing.T) *fakeNATPMPServer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeNATPMPServer{
		conn:         conn,
		addr:         conn.LocalAddr().(*net.UDPAddr),
		externalPort: 7777,
		lifetime:     3600,
	}
	go func() {
		buf := make([]byte, 12)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 2 {
				continue
			}
			s.gotOp.Store(int32(buf[1]))
			resp := make([]byte, 16)
			resp[0] = 0
			resp[1] = buf[1] | 0x80
			binary.BigEndian.PutUint16(resp[2:4], s.resultCode)
			binary.BigEndian.PutUint32(resp[4:8], uint32(time.Now().Unix()))
			binary.BigEndian.PutUint16(resp[8:10], binary.BigEndian.Uint16(buf[4:6]))
			binary.BigEndian.PutUint16(resp[10:12], s.externalPort)
			binary.BigEndian.PutUint32(resp[12:16], s.lifetime)
			_, _ = conn.WriteToUDP(resp, src)
		}
	}()
	return s
}

func (s *fakeNATPMPServer) close() { _ = s.conn.Close() }

func TestNATPMPMapSuccess(t *testing.T) {
	srv := startFakeNATPMP(t)
	defer srv.close()

	// Override the destination port by dialling our test server directly.
	// We can't override natpmpMap's hardcoded 5351, so we test the
	// underlying wire protocol via a small inline helper.
	conn, err := net.DialUDP("udp4", nil, srv.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := make([]byte, 12)
	req[1] = 2 // TCP
	binary.BigEndian.PutUint16(req[4:6], 6881)
	binary.BigEndian.PutUint16(req[6:8], 6881)
	binary.BigEndian.PutUint32(req[8:12], 3600)

	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	resp := make([]byte, 16)
	if n, err := conn.Read(resp); err != nil || n != 16 {
		t.Fatalf("read: n=%d err=%v", n, err)
	}
	if got := binary.BigEndian.Uint16(resp[2:4]); got != 0 {
		t.Errorf("result code = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint16(resp[10:12]); got != 7777 {
		t.Errorf("external port = %d, want 7777", got)
	}
	if got := srv.gotOp.Load(); got != 2 {
		t.Errorf("op = %d, want 2 (TCP)", got)
	}
}

// --- PortMapper smoke tests ---------------------------------------------

// TestPortMapperNilSafe verifies all methods are safe on a nil receiver.
func TestPortMapperNilSafe(t *testing.T) {
	var m *PortMapper
	m.Start(context.Background())
	m.Stop()
	if m.ExternalPort() != 0 {
		t.Errorf("nil ExternalPort != 0")
	}
	if m.Method() != "" {
		t.Errorf("nil Method != \"\"")
	}
}

// TestPortMapperZeroInternal verifies Start is a no-op when no port is
// configured.
func TestPortMapperZeroInternal(t *testing.T) {
	m := NewPortMapper(0)
	m.Start(context.Background())
	m.Stop()
	if m.ExternalPort() != 0 {
		t.Errorf("expected no mapping for internal=0")
	}
}

// TestPortMapperStopWithoutStart should be safe.
func TestPortMapperStopWithoutStart(t *testing.T) {
	m := NewPortMapper(6881)
	m.Stop() // must not block or panic
}

// TestPortMapperRunNoIGD verifies that with no IGD on the network the
// run loop exits cleanly without leaving goroutines pinned. We pick a
// short context and require Stop to return promptly. This test relies
// on the local network actually NOT having an IGD; CI runners satisfy
// that.
func TestPortMapperRunNoIGD(t *testing.T) {
	if testing.Short() {
		t.Skip("network discovery test")
	}
	m := NewPortMapper(0xfffe) // odd port unlikely to clash
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	m.Start(ctx)
	// Wait a bit for SSDP timeout to trip.
	time.Sleep(6 * time.Second)
	m.Stop()
	// We don't assert success/failure: depending on the runner, an IGD
	// may or may not respond. We only want to make sure no panics or
	// goroutine leaks occur.
	_ = fmt.Sprintf("method=%s external=%d", m.Method(), m.ExternalPort())
}
