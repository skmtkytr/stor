package download

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
)

// PeerSnap is a point-in-time snapshot of a connected peer for UI display.
type PeerSnap struct {
	Addr      string  `json:"addr"`       // "host:port"
	IPVersion int     `json:"ip_version"` // 4 or 6
	Incoming  bool    `json:"incoming"`   // true if peer dialed us
	UsingUTP  bool    `json:"using_utp"`  // true if uTP transport
	Encrypted bool    `json:"encrypted"`  // true if MSE/PE negotiated
	Client    string  `json:"client"`     // decoded client name (e.g. "qBittorrent 4.6.0")
	PeerID    string  `json:"peer_id"`    // raw peer ID, shown as escaped string
	DownRate  float64 `json:"down_rate"`  // bytes/sec (downloading from peer)
	UpRate    float64 `json:"up_rate"`    // bytes/sec (uploading to peer)
	Choked    bool    `json:"choked"`     // remote choked us
	Choking   bool    `json:"choking"`    // we choked remote
	Progress  float64 `json:"progress"`   // 0.0-1.0, peer's piece completion
}

// Snapshot produces a PeerSnap for display. Cheap to call from any goroutine.
func (c *Client) Snapshot(numPieces int) PeerSnap {
	c.stateMu.Lock()
	choked := c.choked
	choking := c.choking
	c.stateMu.Unlock()

	ipVer := 4
	if host, _, err := net.SplitHostPort(c.Addr); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			ipVer = 6
		}
	}

	var progress float64
	if numPieces > 0 {
		have := 0
		if c.haveAll {
			have = numPieces
		} else if c.bitfield != nil {
			for i := range numPieces {
				if c.bitfield.HasPiece(i) {
					have++
				}
			}
		}
		progress = float64(have) / float64(numPieces)
	}

	// Prefer the short-window EMA (same 1-second tick + smoothing factor
	// as the torrent-wide Progress EMA) so per-peer UI rates stay in
	// sync with the total shown in the top row. The EMA is written by
	// the shared rateRegistry ticker into downRate/upRate. Fall back to
	// the cumulative-over-rechoke-window Speed()/UploadSpeed() for
	// zero-value test clients that never registered with the rate
	// sampler.
	var downRate, upRate float64
	if c.unregRate != nil {
		downRate = float64(c.downRate.Load())
		upRate = float64(c.upRate.Load())
	} else {
		downRate = c.Speed()
		upRate = c.UploadSpeed()
	}

	return PeerSnap{
		Addr:      c.Addr,
		IPVersion: ipVer,
		Incoming:  c.Incoming,
		UsingUTP:  c.UsingUTP,
		Encrypted: c.Encrypted,
		Client:    DecodePeerID(c.RemotePeerID),
		PeerID:    strings.TrimRight(string(c.RemotePeerID[:]), "\x00"),
		DownRate:  downRate,
		UpRate:    upRate,
		Choked:    choked,
		Choking:   choking,
		Progress:  progress,
	}
}

// Snapshot returns a snapshot of all peers currently registered with the manager.
func (pm *PeerManager) Snapshot(numPieces int) []PeerSnap {
	pm.mu.Lock()
	peers := make([]*Client, len(pm.peers))
	copy(peers, pm.peers)
	pm.mu.Unlock()

	out := make([]PeerSnap, 0, len(peers))
	for _, c := range peers {
		out = append(out, c.Snapshot(numPieces))
	}
	return out
}

// Snapshot returns snapshots of all incoming peers served by the uploader.
func (u *Uploader) Snapshot(numPieces int) []PeerSnap {
	u.mu.Lock()
	peers := make([]*Client, len(u.clients))
	copy(peers, u.clients)
	u.mu.Unlock()

	out := make([]PeerSnap, 0, len(peers))
	for _, c := range peers {
		out = append(out, c.Snapshot(numPieces))
	}
	return out
}

// azureusClientNames maps the 2-letter Azureus-style peer ID code to a
// human-readable client name. This covers the most common BitTorrent clients.
var azureusClientNames = map[string]string{
	"7T": "aTorrent",
	"AB": "AnyEvent::BitTorrent",
	"AT": "Artemis",
	"AX": "BitPump",
	"AZ": "Azureus",
	"BB": "BitBuddy",
	"BC": "BitComet",
	"BE": "BitTorrent SDK",
	"BF": "BitFlu",
	"BG": "BTG",
	"BI": "BiglyBT",
	"BL": "BitBlinder",
	"BP": "BitTorrent Pro",
	"BR": "BitRocket",
	"BS": "BTSlave",
	"BT": "BitTorrent",
	"BW": "BitWombat",
	"BX": "BittorrentX",
	"CD": "Enhanced CTorrent",
	"CT": "CTorrent",
	"DE": "Deluge",
	"DP": "Propagate Data Client",
	"EB": "EBit",
	"ES": "electric sheep",
	"FC": "FileCroc",
	"FD": "Free Download Manager",
	"FT": "FoxTorrent",
	"FW": "FrostWire",
	"FX": "Freebox BitTorrent",
	"GS": "GSTorrent",
	"HK": "Hekate",
	"HL": "Halite",
	"HN": "Hydranode",
	"KG": "KGet",
	"KT": "KTorrent",
	"LC": "LeechCraft",
	"LH": "LH-ABC",
	"LP": "Lphant",
	"LT": "libtorrent",
	"lt": "libTorrent",
	"LW": "LimeWire",
	"MK": "Meerkat",
	"MO": "MonoTorrent",
	"MP": "MooPolice",
	"MR": "Miro",
	"MT": "MoonlightTorrent",
	"NB": "Net::BitTorrent",
	"NX": "Net Transport",
	"OS": "OneSwarm",
	"OT": "OmegaTorrent",
	"PB": "Protocol::BitTorrent",
	"PD": "Pando",
	"PI": "PicoTorrent",
	"qB": "qBittorrent",
	"QD": "QQDownload",
	"RT": "Retriever",
	"RZ": "RezTorrent",
	"S~": "Shareaza alpha/beta",
	"SB": "Swiftbit",
	"SD": "Thunder",
	"SM": "SoMud",
	"SP": "BitSpirit",
	"SS": "SwarmScope",
	"ST": "stor",
	"st": "SharkTorrent",
	"SZ": "Shareaza",
	"TB": "Torch",
	"TE": "terasaur Seed Bank",
	"TL": "Tribler",
	"TN": "TorrentDotNET",
	"TR": "Transmission",
	"TS": "Torrentstorm",
	"TT": "TuoTu",
	"UL": "uLeecher!",
	"UM": "\u00b5Torrent Mac",
	"UT": "\u00b5Torrent",
	"VG": "Vagaa",
	"WD": "WebTorrent Desktop",
	"WT": "BitLet",
	"WW": "WebTorrent",
	"WY": "FireTorrent",
	"XF": "Xfplay",
	"XL": "Xunlei",
	"XS": "XSwifter",
	"XT": "XanTorrent",
	"XX": "Xtorrent",
	"ZT": "ZipTorrent",
}

// DecodePeerID returns a human-readable client name decoded from the 20-byte
// peer ID. Supports the Azureus-style format (-XX0000-...) used by most modern
// clients. Returns "Unknown" when the format is not recognized.
func DecodePeerID(id [20]byte) string {
	if id == ([20]byte{}) {
		return "Unknown"
	}

	// Azureus-style: first char '-', chars 1-2 = client code, chars 3-6 = version digits/letters, char 7 = '-'
	if id[0] == '-' && id[7] == '-' {
		code := string(id[1:3])
		name, ok := azureusClientNames[code]
		if !ok {
			return fmt.Sprintf("Unknown (%s)", code)
		}
		version := decodeAzureusVersion(id[3:7])
		if version == "" {
			return name
		}
		return name + " " + version
	}

	// Shadow-style: first char is uppercase letter (client code), followed by
	// 5 version chars (alphanumeric), then 14 random bytes. e.g. "A12345-".
	if id[0] >= 'A' && id[0] <= 'Z' && id[6] == '-' {
		code := string(id[0])
		name := shadowClientNames[code]
		if name == "" {
			name = "Unknown"
		}
		return name
	}

	// Mainline-style: "M<version>--" (e.g. "M3-4-2--")
	if id[0] == 'M' && id[2] == '-' {
		return "Mainline " + string(id[1:6])
	}

	return "Unknown"
}

// shadowClientNames for the single-letter Shadow-style encoding.
var shadowClientNames = map[string]string{
	"A": "ABC",
	"O": "Osprey Permaseed",
	"Q": "BTQueue",
	"R": "Tribler",
	"S": "Shadow",
	"T": "BitTornado",
	"U": "UPnP NAT BT",
}

// decodeAzureusVersion decodes a 4-byte Azureus version string like "4600"
// into "4.6.0" or "1234" into "1.2.34". Non-digit characters are kept as-is.
func decodeAzureusVersion(v []byte) string {
	// Trim trailing non-version bytes (rare but defensive)
	allDigits := true
	for _, b := range v {
		if !unicode.IsDigit(rune(b)) {
			allDigits = false
			break
		}
	}
	if allDigits && len(v) == 4 {
		// 4-digit: XYZW → X.Y.Z if W is 0, else X.Y.ZW
		major := string(v[0])
		minor := string(v[1])
		patch := string(v[2:4])
		if v[3] == '0' {
			patch = string(v[2])
		}
		// Remove leading zero from patch
		if n, err := strconv.Atoi(patch); err == nil {
			patch = strconv.Itoa(n)
		}
		return fmt.Sprintf("%s.%s.%s", major, minor, patch)
	}
	// Special cases: libtorrent uses 4-digit combined (e.g. "1234" = 1.2.34)
	// We render as X.Y.ZZ here if all digits.
	if allDigits && len(v) == 4 {
		return fmt.Sprintf("%c.%c.%s", v[0], v[1], string(v[2:4]))
	}
	return string(v)
}
