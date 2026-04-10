package engine

import (
	"fmt"
	"net"

	"github.com/skmtkytr/stor/tracker"
)

// parsePeerAddrs converts "host:port" strings to tracker.Peer slice.
func parsePeerAddrs(addrs []string) []tracker.Peer {
	var peers []tracker.Peer
	for _, addr := range addrs {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		var port uint16
		if _, scanErr := fmt.Sscanf(portStr, "%d", &port); scanErr == nil {
			peers = append(peers, tracker.Peer{IP: ip, Port: port})
		}
	}
	return peers
}
