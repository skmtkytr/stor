package bencode

import (
	"errors"
	"fmt"
	"testing"
)

func TestDecodeString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple", "5:hello", "hello", false},
		{"empty", "0:", "", false},
		{"longer", "11:hello world", "hello world", false},
		{"invalid length", "5:hi", "", true},
		{"no colon", "5hello", "", true},
		{"negative length", "-1:a", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			s, ok := got.(string)
			if !ok {
				t.Fatalf("expected string, got %T", got)
			}
			if s != tt.want {
				t.Errorf("got %q, want %q", s, tt.want)
			}
		})
	}
}

func TestDecodeInt(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"positive", "i42e", 42, false},
		{"zero", "i0e", 0, false},
		{"negative", "i-1e", -1, false},
		{"large", "i9999999999e", 9999999999, false},
		{"leading zero", "i03e", 0, true},
		{"negative zero", "i-0e", 0, true},
		{"empty", "ie", 0, true},
		{"no end", "i42", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			n, ok := got.(int64)
			if !ok {
				t.Fatalf("expected int64, got %T", got)
			}
			if n != tt.want {
				t.Errorf("got %d, want %d", n, tt.want)
			}
		})
	}
}

func TestDecodeList(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{"empty", "le", 0, false},
		{"strings", "l5:hello5:worlde", 2, false},
		{"mixed", "l5:helloi42ee", 2, false},
		{"nested", "ll5:helloee", 1, false},
		{"no end", "l5:hello", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			l, ok := got.([]any)
			if !ok {
				t.Fatalf("expected []any, got %T", got)
			}
			if len(l) != tt.wantLen {
				t.Errorf("got len %d, want %d", len(l), tt.wantLen)
			}
		})
	}
}

func TestDecodeDict(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
		wantErr bool
	}{
		{"empty", "de", 0, false},
		{"simple", "d3:cow3:moo4:spam4:eggse", 2, false},
		{"nested", "d4:infod4:name4:testee", 1, false},
		{"non-string key", "di1e3:fooe", 0, true},
		{"unsorted keys", "d4:spam4:eggs3:cow3:mooe", 0, true},
		{"duplicate keys", "d3:cow3:moo3:cow4:moose", 1, false},
		{"no end", "d3:cow3:moo", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			d, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("expected map[string]any, got %T", got)
			}
			if len(d) != tt.wantLen {
				t.Errorf("got len %d, want %d", len(d), tt.wantLen)
			}
		})
	}
}

func TestDecodeStringExceedsMaxLength(t *testing.T) {
	// A bencode string header claiming 100MB should be rejected with
	// ErrInvalidFormat (limit violation), not ErrUnexpectedEnd (truncated data).
	input := []byte("104857601:x") // 100*1024*1024 + 1 = exceeds 64MB limit
	_, err := Decode(input)
	if err == nil {
		t.Fatal("expected error for string length exceeding limit")
	}
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidFormat, got: %v", err)
	}
}

func TestDecodeListExceedsMaxItems(t *testing.T) {
	// Build a bencode list with MaxCollectionSize+1 items: l i0e i0e ... e
	// Each item is "i0e" (3 bytes). To avoid building a huge buffer,
	// we test with a small limit by using a helper.
	// Instead, build a list of 1_000_001 "i0e" items.
	// This is ~3MB, acceptable for a test.
	var buf []byte
	buf = append(buf, 'l')
	item := []byte("i0e")
	for range MaxCollectionSize + 1 {
		buf = append(buf, item...)
	}
	buf = append(buf, 'e')

	_, err := Decode(buf)
	if err == nil {
		t.Fatal("expected error for list exceeding max items")
	}
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidFormat, got: %v", err)
	}
}

func TestDecodeDictExceedsMaxItems(t *testing.T) {
	// Build a bencode dict with MaxCollectionSize+1 entries.
	// Keys must be sorted, so use zero-padded numeric keys.
	var buf []byte
	buf = append(buf, 'd')
	for i := range MaxCollectionSize + 1 {
		key := fmt.Sprintf("%07d", i) // 7-char key, always sorted
		buf = append(buf, fmt.Sprintf("%d:%s", len(key), key)...)
		buf = append(buf, "i0e"...)
	}
	buf = append(buf, 'e')

	_, err := Decode(buf)
	if err == nil {
		t.Fatal("expected error for dict exceeding max items")
	}
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidFormat, got: %v", err)
	}
}

func TestDecodeExceedsMaxDepth(t *testing.T) {
	// Build deeply nested lists: l l l ... e e e
	depth := 150 // exceeds MaxDecodeDepth (100)
	var buf []byte
	for range depth {
		buf = append(buf, 'l')
	}
	for range depth {
		buf = append(buf, 'e')
	}

	_, err := Decode(buf)
	if err == nil {
		t.Fatal("expected error for nesting depth exceeding limit")
	}
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidFormat, got: %v", err)
	}
}

func TestDecodeDictValues(t *testing.T) {
	got, err := Decode([]byte("d3:cow3:moo4:spam4:eggse"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	d := got.(map[string]any)
	if d["cow"] != "hello"[:0]+"moo" {
		t.Errorf("cow: got %q, want %q", d["cow"], "moo")
	}
	if d["spam"] != "eggs" {
		t.Errorf("spam: got %q, want %q", d["spam"], "eggs")
	}
}
