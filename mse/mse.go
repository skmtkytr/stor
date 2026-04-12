// Package mse implements Message Stream Encryption (MSE/PE) as used by
// BitTorrent clients to obfuscate protocol traffic and avoid ISP throttling.
//
// The protocol uses Diffie-Hellman key exchange with a 768-bit prime,
// followed by RC4 encryption of the stream. Both "encrypt headers only"
// (plaintext) and "full stream" (RC4) modes are supported.
package mse

import (
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

// The 768-bit prime used for DH key exchange (same as libtorrent / Azureus / Vuze).
var dhPrime = func() *big.Int {
	p, _ := new(big.Int).SetString(
		"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1"+
			"29024E088A67CC74020BBEA63B139B22514A08798E3404DD"+
			"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245"+
			"E485B576625E7EC6F44C42E9A63A36210000000000090563", 16)
	return p
}()

var dhG = big.NewInt(2)

// CryptoMethod selects the payload encryption method.
type CryptoMethod uint32

const (
	CryptoPlaintext CryptoMethod = 0x01
	CryptoRC4       CryptoMethod = 0x02
)

// Conn wraps a net.Conn with optional RC4 encryption.
type Conn struct {
	net.Conn
	r *rc4.Cipher // decrypt incoming
	w *rc4.Cipher // encrypt outgoing
}

func (c *Conn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && c.r != nil {
		c.r.XORKeyStream(b[:n], b[:n])
	}
	return n, err
}

func (c *Conn) Write(b []byte) (int, error) {
	if c.w == nil {
		return c.Conn.Write(b)
	}
	buf := make([]byte, len(b))
	c.w.XORKeyStream(buf, b)
	return c.Conn.Write(buf)
}

// HandshakeOutgoing performs MSE handshake as the initiator (connecting party).
// infoHash is required to derive encryption keys.
// Returns an encrypted Conn and the crypto method negotiated.
func HandshakeOutgoing(conn net.Conn, infoHash [20]byte, preferred CryptoMethod) (*Conn, CryptoMethod, error) {
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	// Step 1: DH key exchange
	privKey, pubKey := generateDHKeyPair()

	// Send Ya (our public key, padded to 96 bytes)
	yaBytes := padTo(pubKey.Bytes(), 96)
	if _, err := conn.Write(yaBytes); err != nil {
		return nil, 0, fmt.Errorf("mse: write Ya: %w", err)
	}

	// Read Yb (peer's public key)
	ybBytes := make([]byte, 96)
	if _, err := io.ReadFull(conn, ybBytes); err != nil {
		return nil, 0, fmt.Errorf("mse: read Yb: %w", err)
	}
	yb := new(big.Int).SetBytes(ybBytes)

	// Compute shared secret S = Yb^Xa mod P
	s := new(big.Int).Exp(yb, privKey, dhPrime)
	sBytes := padTo(s.Bytes(), 96)

	// Step 2: Derive keys
	// HASH('keyA', S, SKEY) and HASH('keyB', S, SKEY)
	reqRC4Key := deriveKey("keyA", sBytes, infoHash[:])
	respRC4Key := deriveKey("keyB", sBytes, infoHash[:])

	// Step 3: Send encrypted handshake
	// req1 = HASH('req1', S)
	// req2 = HASH('req2', SKEY) XOR HASH('req3', S)
	req1 := hashBytes("req1", sBytes)
	req2 := xorBytes(hashBytes("req2", infoHash[:]), hashBytes("req3", sBytes))

	// VC = 8 zero bytes
	vc := make([]byte, 8)

	// Create initiator encrypt cipher, discard first 1024 bytes
	encCipher, _ := rc4.NewCipher(reqRC4Key)
	discard := make([]byte, 1024)
	encCipher.XORKeyStream(discard, discard)

	// crypto_provide = our preferred method
	provide := make([]byte, 4)
	binary.BigEndian.PutUint32(provide, uint32(preferred|CryptoPlaintext))

	// padC (random 0-512 bytes)
	padC := randomPad()
	padCLen := make([]byte, 2)
	binary.BigEndian.PutUint16(padCLen, uint16(len(padC)))

	// Build message: HASH('req1', S) + HASH('req2', SKEY) XOR HASH('req3', S) + ENCRYPT(VC + crypto_provide + len(padC) + padC + len(IA)=0)
	// IA (initial payload) is empty for simplicity
	ia := []byte{}
	iaLen := make([]byte, 2)

	// Encrypt: VC + crypto_provide + len(padC) + padC + len(IA) + IA
	plainPayload := concat(vc, provide, padCLen, padC, iaLen, ia)
	encPayload := make([]byte, len(plainPayload))
	encCipher.XORKeyStream(encPayload, plainPayload)

	msg := concat(req1, req2, encPayload)
	if _, err := conn.Write(msg); err != nil {
		return nil, 0, fmt.Errorf("mse: write handshake: %w", err)
	}

	// Step 4: Read peer's response
	// Create responder decrypt cipher, discard first 1024 bytes
	decCipher, _ := rc4.NewCipher(respRC4Key)
	discard2 := make([]byte, 1024)
	decCipher.XORKeyStream(discard2, discard2)

	// Read encrypted VC (8 bytes) — scan for 8 zero bytes after decryption
	// In practice, we need to scan up to 616 bytes (512 pad + 8 VC + 96 overhead)
	scanBuf := make([]byte, 616)
	n, err := io.ReadAtLeast(conn, scanBuf, 8)
	if err != nil {
		return nil, 0, fmt.Errorf("mse: read response: %w", err)
	}
	decBuf := make([]byte, n)
	decCipher.XORKeyStream(decBuf, scanBuf[:n])

	// Find VC (8 zero bytes) in decrypted buffer
	vcIdx := -1
	for i := 0; i <= n-8; i++ {
		allZero := true
		for j := range 8 {
			if decBuf[i+j] != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			vcIdx = i
			break
		}
	}
	if vcIdx < 0 {
		return nil, 0, fmt.Errorf("mse: VC not found in response")
	}

	// After VC: crypto_select (4 bytes) + len(padD) (2 bytes) + padD
	remaining := decBuf[vcIdx+8:]
	if len(remaining) < 6 {
		extra := make([]byte, 6-len(remaining))
		if _, err := io.ReadFull(conn, extra); err != nil {
			return nil, 0, fmt.Errorf("mse: read crypto_select: %w", err)
		}
		decExtra := make([]byte, len(extra))
		decCipher.XORKeyStream(decExtra, extra)
		remaining = append(remaining, decExtra...)
	}

	cryptoSelect := CryptoMethod(binary.BigEndian.Uint32(remaining[:4]))
	padDLen := binary.BigEndian.Uint16(remaining[4:6])

	if padDLen > 0 {
		padD := make([]byte, padDLen)
		got := copy(padD, remaining[6:])
		if got < int(padDLen) {
			rest := make([]byte, int(padDLen)-got)
			if _, err := io.ReadFull(conn, rest); err != nil {
				return nil, 0, fmt.Errorf("mse: read padD: %w", err)
			}
			decRest := make([]byte, len(rest))
			decCipher.XORKeyStream(decRest, rest)
			copy(padD[got:], decRest)
		}
	}

	// Build encrypted connection
	ec := &Conn{Conn: conn}
	if cryptoSelect == CryptoRC4 {
		ec.r = decCipher
		ec.w = encCipher
	}
	// For CryptoPlaintext, r and w are nil — reads/writes pass through

	return ec, cryptoSelect, nil
}

// --- helpers ---

func generateDHKeyPair() (*big.Int, *big.Int) {
	// Private key: random 768-bit number (matches DH prime size)
	privBytes := make([]byte, 96)
	_, _ = rand.Read(privBytes)
	priv := new(big.Int).SetBytes(privBytes)

	// Public key: g^priv mod p
	pub := new(big.Int).Exp(dhG, priv, dhPrime)
	return priv, pub
}

func padTo(b []byte, size int) []byte {
	if len(b) >= size {
		return b[:size]
	}
	pad := make([]byte, size)
	copy(pad[size-len(b):], b)
	return pad
}

func hashBytes(prefix string, data ...[]byte) []byte {
	h := sha1.New()
	h.Write([]byte(prefix))
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

func deriveKey(prefix string, s, skey []byte) []byte {
	h := sha1.New()
	h.Write([]byte(prefix))
	h.Write(s)
	h.Write(skey)
	return h.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	out := make([]byte, n)
	for i := range n {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func randomPad() []byte {
	lenBuf := make([]byte, 2)
	_, _ = rand.Read(lenBuf)
	padLen := int(binary.BigEndian.Uint16(lenBuf)) % 512
	pad := make([]byte, padLen)
	_, _ = rand.Read(pad)
	return pad
}

func concat(parts ...[]byte) []byte {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
