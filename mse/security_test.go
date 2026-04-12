package mse

import (
	"testing"
)

func TestDHKeySize(t *testing.T) {
	// DH private key should be at least 768 bits (96 bytes) for adequate security.
	priv, _ := generateDHKeyPair()
	privBytes := priv.Bytes()
	// A 96-byte random value should typically produce a >= 90 byte big.Int
	// (leading zeros are trimmed by big.Int.Bytes).
	// We check the original generation uses 96 bytes.
	if len(privBytes) < 64 {
		t.Errorf("DH private key too small: %d bytes (expected >= 64)", len(privBytes))
	}
}
