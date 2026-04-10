package mse

import (
	"bytes"
	"crypto/rc4"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestDHKeyExchange(t *testing.T) {
	privA, pubA := generateDHKeyPair()
	privB, pubB := generateDHKeyPair()

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
	key2 := deriveKey("keyA", s, skey)
	if !bytes.Equal(key, key2) {
		t.Fatal("key derivation not deterministic")
	}
	key3 := deriveKey("keyB", s, skey)
	if bytes.Equal(key, key3) {
		t.Fatal("different prefixes should give different keys")
	}
}

func TestConnReadWriteRC4(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	key := hashBytes("testkey", []byte("shared-secret"))

	// Same RC4 key for both directions (simplified for testing)
	wCipher, _ := rc4.NewCipher(key)
	rCipher, _ := rc4.NewCipher(key)

	ec := &Conn{Conn: clientConn, w: wCipher}
	es := &Conn{Conn: serverConn, r: rCipher}

	msg := []byte("hello encrypted world")
	done := make(chan error, 1)

	go func() {
		_, err := ec.Write(msg)
		done <- err
	}()

	buf := make([]byte, len(msg))
	n, err := io.ReadFull(es, buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("decrypted: %q, want %q", buf, msg)
	}
	_ = n

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeOutgoingTimeout(t *testing.T) {
	// Server that doesn't respond — should timeout
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			// Read but don't respond
			buf := make([]byte, 1024)
			_, _ = conn.Read(buf)
			time.Sleep(20 * time.Second)
			_ = conn.Close()
		}
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	infoHash := [20]byte{1, 2, 3, 4, 5}
	_, _, err = HandshakeOutgoing(conn, infoHash, CryptoRC4)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	_ = conn.Close()
}

func TestConcat(t *testing.T) {
	result := concat([]byte{1, 2}, []byte{3}, []byte{4, 5, 6})
	expected := []byte{1, 2, 3, 4, 5, 6}
	if !bytes.Equal(result, expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
}

func TestRandomPad(t *testing.T) {
	pad := randomPad()
	if len(pad) > 512 {
		t.Fatalf("pad too long: %d", len(pad))
	}
}
