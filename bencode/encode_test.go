package bencode

import (
	"testing"
)

func TestEncodeString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello", "5:hello"},
		{"empty", "", "0:"},
		{"with space", "hello world", "11:hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeInt(t *testing.T) {
	tests := []struct {
		name  string
		input int64
		want  string
	}{
		{"positive", 42, "i42e"},
		{"zero", 0, "i0e"},
		{"negative", -1, "i-1e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeList(t *testing.T) {
	tests := []struct {
		name  string
		input []any
		want  string
	}{
		{"empty", []any{}, "le"},
		{"strings", []any{"hello", "world"}, "l5:hello5:worlde"},
		{"mixed", []any{"hello", int64(42)}, "l5:helloi42ee"},
		{"nested", []any{[]any{"hello"}}, "ll5:helloee"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeDict(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"empty", map[string]any{}, "de"},
		{"simple", map[string]any{"cow": "moo", "spam": "eggs"}, "d3:cow3:moo4:spam4:eggse"},
		{"nested", map[string]any{"info": map[string]any{"name": "test"}}, "d4:infod4:name4:testee"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Encode(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	inputs := []string{
		"5:hello",
		"i42e",
		"l5:helloi42ee",
		"d3:cow3:moo4:spam4:eggse",
		"d4:infod6:lengthi1024e4:name4:testee",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			val, err := Decode([]byte(input))
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			encoded, err := Encode(val)
			if err != nil {
				t.Fatalf("encode error: %v", err)
			}
			if string(encoded) != input {
				t.Errorf("roundtrip failed: got %q, want %q", encoded, input)
			}
		})
	}
}
