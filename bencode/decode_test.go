package bencode

import (
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
