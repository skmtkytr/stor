package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// validWebSeedURL checks that a webseed URL uses http or https scheme
// and does not point to a private/internal IP address (SSRF prevention).
func validWebSeedURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}

	host := u.Hostname()
	if host == "localhost" {
		return false
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Non-IP hostname (e.g. "example.com") — allow.
		// DNS resolution will happen at request time.
		return true
	}

	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

// downloadPieceHTTP downloads a single piece from a webseed URL using HTTP Range.
func downloadPieceHTTP(ctx context.Context, baseURL string, tf *torrent.TorrentFile, pw PieceWork) ([]byte, error) {
	pieceLen := int64(tf.Info.PieceLength)
	offset := int64(pw.Index) * pieceLen

	var buf []byte
	if !storage.IsMultiFile(tf) {
		data, err := httpRange(ctx, baseURL, offset, int64(pw.Length))
		if err != nil {
			return nil, err
		}
		buf = data
	} else {
		buf = make([]byte, pw.Length)
		written := 0
		remaining := int64(pw.Length)
		pos := offset

		var fileOffset int64
		for _, f := range tf.Info.Files {
			fileEnd := fileOffset + f.Length
			if pos >= fileEnd {
				fileOffset = fileEnd
				continue
			}

			localOff := pos - fileOffset
			canRead := f.Length - localOff
			if canRead > remaining {
				canRead = remaining
			}

			url := strings.TrimRight(baseURL, "/") + "/" + strings.Join(f.Path, "/")
			data, err := httpRange(ctx, url, localOff, canRead)
			if err != nil {
				return nil, fmt.Errorf("webseed: %s: %w", f.Path[len(f.Path)-1], err)
			}
			if written+len(data) > len(buf) {
				return nil, fmt.Errorf("webseed: piece %d multifile data exceeds buffer (written=%d, data=%d, buf=%d)", pw.Index, written, len(data), len(buf))
			}
			copy(buf[written:], data)
			written += len(data)
			remaining -= int64(len(data))
			pos += int64(len(data))
			fileOffset = fileEnd

			if remaining <= 0 {
				break
			}
		}
	}

	hash := sha1.Sum(buf)
	if hash != pw.Hash {
		return nil, fmt.Errorf("webseed: piece %d hash mismatch", pw.Index)
	}
	return buf, nil
}

// resolveAndValidateHost resolves a hostname and returns a validated public IP.
// Returns error if the host resolves to a private/loopback address.
func resolveAndValidateHost(host string) (string, error) {
	if host == "localhost" {
		return "", fmt.Errorf("host %q is localhost", host)
	}
	// If already an IP, validate directly
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return "", fmt.Errorf("host %s is a private address", host)
		}
		return ip.String(), nil
	}
	// Resolve hostname
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return "", fmt.Errorf("host %s resolves to private address %s", host, addr)
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("host %s has no valid addresses", host)
}

// isPrivateHost returns true if the host is a private/loopback/link-local address.
func isPrivateHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}
	// Resolve hostname and check all addresses
	addrs, err := net.LookupHost(host)
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if resolved := net.ParseIP(addr); resolved != nil {
			if resolved.IsLoopback() || resolved.IsPrivate() || resolved.IsLinkLocalUnicast() {
				return true
			}
		}
	}
	return false
}

func httpRange(ctx context.Context, targetURL string, offset, length int64) ([]byte, error) {
	// DNS pinning: resolve hostname once and validate, then connect to the
	// resolved IP directly. This prevents DNS rebinding SSRF attacks.
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	resolvedIP, err := resolveAndValidateHost(host)
	if err != nil {
		return nil, fmt.Errorf("webseed: %w", err)
	}

	// Build a client that dials the resolved IP directly (DNS pinning)
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	pinnedAddr := net.JoinHostPort(resolvedIP, port)
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, pinnedAddr)
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("webseed: expected HTTP 206, got %d for %s", resp.StatusCode, targetURL)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, length))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) < length {
		return nil, fmt.Errorf("webseed: short read: got %d, want %d", len(data), length)
	}
	return data, nil
}
