package dht

import "testing"

// TestDHTDoubleClose verifies that calling Close() twice does not panic.
func TestDHTDoubleClose(t *testing.T) {
	d, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	// First close should succeed
	if err := d.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	// Second close must not panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Close() panicked: %v", r)
		}
	}()
	_ = d.Close()
}
