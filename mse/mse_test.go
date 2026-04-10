package mse

import (
	"math/big"
	"testing"
)

func TestDHKeyExchange(t *testing.T) {
	// Two parties generate key pairs
	privA, pubA := generateDHKeyPair()
	privB, pubB := generateDHKeyPair()

	// Shared secret should match
	sA := new(big.Int).Exp(pubB, privA, dhPrime)
	sB := new(big.Int).Exp(pubA, privB, dhPrime)

	if sA.Cmp(sB) != 0 {
		t.Fatal("DH shared secrets don't match")
	}
}

func TestPadTo(t *testing.T) {
	b := []byte{1, 2, 3}
	padded := padTo(b, 5)
	if len(padded) != 5 {
		t.Fatalf("expected 5, got %d", len(padded))
	}
	// Should be zero-padded on the left
	if padded[0] != 0 || padded[1] != 0 || padded[2] != 1 || padded[3] != 2 || padded[4] != 3 {
		t.Fatalf("unexpected padding: %v", padded)
	}
}

func TestXorBytes(t *testing.T) {
	a := []byte{0xff, 0x00, 0xaa}
	b := []byte{0x0f, 0xf0, 0x55}
	result := xorBytes(a, b)
	expected := []byte{0xf0, 0xf0, 0xff}
	for i, v := range result {
		if v != expected[i] {
			t.Fatalf("mismatch at %d: got %02x, want %02x", i, v, expected[i])
		}
	}
}

func TestDeriveKey(t *testing.T) {
	s := make([]byte, 96)
	skey := make([]byte, 20)
	key := deriveKey("keyA", s, skey)
	if len(key) != 20 {
		t.Fatalf("expected 20 bytes, got %d", len(key))
	}
	// Should be deterministic
	key2 := deriveKey("keyA", s, skey)
	for i := range key {
		if key[i] != key2[i] {
			t.Fatal("key derivation not deterministic")
		}
	}
	// Different prefix should give different key
	key3 := deriveKey("keyB", s, skey)
	same := true
	for i := range key {
		if key[i] != key3[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different prefixes should give different keys")
	}
}
