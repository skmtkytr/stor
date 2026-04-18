package download

import (
	"testing"
)

func TestDecodePeerIDAzureus(t *testing.T) {
	// Azureus-style: -XX0000-... where XX is 2-char client code, 0000 is version
	tests := []struct {
		id   string
		want string
	}{
		{"-qB4600-abcdefghijkl", "qBittorrent 4.6.0"},
		{"-TR3000-abcdefghijkl", "Transmission 3.0.0"},
		{"-UT3500-abcdefghijkl", "\u00b5Torrent 3.5.0"},
		{"-DE2000-abcdefghijkl", "Deluge 2.0.0"},
		{"-LT1234-abcdefghijkl", "libtorrent 1.2.34"},
		{"-ST0001-abcdefghijkl", "stor 0.0.1"}, // our own
	}
	for _, tt := range tests {
		var id [20]byte
		copy(id[:], tt.id)
		got := DecodePeerID(id)
		if got != tt.want {
			t.Errorf("DecodePeerID(%q): got %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestDecodePeerIDUnknown(t *testing.T) {
	// Unknown prefix → fallback to "Unknown"
	var id [20]byte
	copy(id[:], "xxxxxxxxxxxxxxxxxxxx")
	got := DecodePeerID(id)
	if got == "" {
		t.Error("unknown peer ID should return a non-empty label")
	}
}

func TestClientPeerInfoDefaults(t *testing.T) {
	// A freshly constructed Client must have consistent zero values for
	// the new peer-info fields.
	c := &Client{Addr: "1.2.3.4:6881"}
	if c.Incoming {
		t.Error("Incoming should default false")
	}
	if c.Encrypted {
		t.Error("Encrypted should default false")
	}
	if c.UsingUTP {
		t.Error("UsingUTP should default false")
	}
	var zero [20]byte
	if c.RemotePeerID != zero {
		t.Error("RemotePeerID should default zero")
	}
}
