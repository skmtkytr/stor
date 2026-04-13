package dht

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadNodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dht_nodes.dat")

	// Create a DHT node and populate its routing table
	d1, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d1.Close() }()

	// Use IDs spread across different buckets to avoid K-bucket overflow
	ids := []ID{
		{0x01}, {0x02}, {0x04}, {0x08},
		{0x10}, {0x20}, {0x40}, {0x80},
	}
	for i, id := range ids {
		d1.table.Update(&Node{
			ID:   id,
			Addr: net.UDPAddr{IP: net.IPv4(192, 168, 1, byte(i+1)), Port: 6881 + i},
		})
	}
	nodeCount := d1.TableSize()

	if nodeCount != len(ids) {
		t.Fatalf("expected %d nodes, got %d", len(ids), nodeCount)
	}

	// Save
	if err := d1.SaveNodes(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists and has expected size (26 bytes per node)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(26*nodeCount) {
		t.Fatalf("file size: got %d, want %d", info.Size(), 26*nodeCount)
	}

	// Load into a new DHT node
	d2, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()

	loaded, err := d2.LoadNodes(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded != nodeCount {
		t.Errorf("loaded: got %d, want %d", loaded, nodeCount)
	}
	if d2.TableSize() < nodeCount-1 {
		t.Errorf("table size after load: got %d, want ~%d", d2.TableSize(), nodeCount)
	}
}

func TestLoadNodesFileNotExist(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	loaded, err := d.LoadNodes("/nonexistent/path/dht_nodes.dat")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if loaded != 0 {
		t.Errorf("expected 0 loaded, got %d", loaded)
	}
}

func TestSaveNodesEmptyTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dht_nodes.dat")

	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	// Save with empty table should be a no-op (no file created)
	if err := d.SaveNodes(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no file for empty routing table")
	}
}
