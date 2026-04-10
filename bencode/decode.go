package bencode

import (
	"errors"
	"fmt"
	"strconv"
)

var (
	ErrUnexpectedEnd = errors.New("bencode: unexpected end of input")
	ErrInvalidFormat = errors.New("bencode: invalid format")
)

// Decode decodes a bencoded byte slice into a Go value.
// Returns one of: string, int64, []any, map[string]any.
func Decode(data []byte) (any, error) {
	val, _, err := decodeValue(data)
	if err != nil {
		return nil, err
	}
	return val, nil
}

// DecodeBytes is like Decode but also returns the number of bytes consumed.
func DecodeBytes(data []byte) (any, int, error) {
	val, rest, err := decodeValue(data)
	if err != nil {
		return nil, 0, err
	}
	return val, len(data) - len(rest), nil
}

func decodeValue(data []byte) (any, []byte, error) {
	if len(data) == 0 {
		return nil, nil, ErrUnexpectedEnd
	}
	switch {
	case data[0] == 'i':
		return decodeInt(data)
	case data[0] == 'l':
		return decodeList(data)
	case data[0] == 'd':
		return decodeDict(data)
	case data[0] >= '0' && data[0] <= '9':
		return decodeString(data)
	default:
		return nil, nil, fmt.Errorf("%w: unexpected byte %q", ErrInvalidFormat, data[0])
	}
}

func decodeString(data []byte) (string, []byte, error) {
	// Find colon
	colonIdx := -1
	for i, b := range data {
		if b == ':' {
			colonIdx = i
			break
		}
		if b < '0' || b > '9' {
			return "", nil, fmt.Errorf("%w: invalid string length", ErrInvalidFormat)
		}
	}
	if colonIdx == -1 {
		return "", nil, fmt.Errorf("%w: missing colon in string", ErrInvalidFormat)
	}

	length, err := strconv.Atoi(string(data[:colonIdx]))
	if err != nil || length < 0 {
		return "", nil, fmt.Errorf("%w: invalid string length", ErrInvalidFormat)
	}

	start := colonIdx + 1
	end := start + length
	if end > len(data) {
		return "", nil, fmt.Errorf("%w: string length exceeds data", ErrUnexpectedEnd)
	}

	return string(data[start:end]), data[end:], nil
}

func decodeInt(data []byte) (int64, []byte, error) {
	// data[0] == 'i'
	endIdx := -1
	for i := 1; i < len(data); i++ {
		if data[i] == 'e' {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return 0, nil, fmt.Errorf("%w: missing 'e' in integer", ErrUnexpectedEnd)
	}

	numStr := string(data[1:endIdx])
	if numStr == "" {
		return 0, nil, fmt.Errorf("%w: empty integer", ErrInvalidFormat)
	}
	// Reject leading zeros: "00", "03", etc. But "0" is ok.
	if len(numStr) > 1 && numStr[0] == '0' {
		return 0, nil, fmt.Errorf("%w: leading zero in integer", ErrInvalidFormat)
	}
	// Reject negative zero
	if numStr == "-0" {
		return 0, nil, fmt.Errorf("%w: negative zero", ErrInvalidFormat)
	}
	// Reject "-" followed by leading zero like "-03"
	if len(numStr) > 2 && numStr[0] == '-' && numStr[1] == '0' {
		return 0, nil, fmt.Errorf("%w: leading zero in negative integer", ErrInvalidFormat)
	}

	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", ErrInvalidFormat, err)
	}

	return n, data[endIdx+1:], nil
}

func decodeList(data []byte) ([]any, []byte, error) {
	// data[0] == 'l'
	rest := data[1:]
	var list []any

	for {
		if len(rest) == 0 {
			return nil, nil, fmt.Errorf("%w: missing 'e' in list", ErrUnexpectedEnd)
		}
		if rest[0] == 'e' {
			return list, rest[1:], nil
		}
		val, remaining, err := decodeValue(rest)
		if err != nil {
			return nil, nil, err
		}
		list = append(list, val)
		rest = remaining
	}
}

func decodeDict(data []byte) (map[string]any, []byte, error) {
	// data[0] == 'd'
	rest := data[1:]
	dict := make(map[string]any)
	var prevKey string

	for {
		if len(rest) == 0 {
			return nil, nil, fmt.Errorf("%w: missing 'e' in dict", ErrUnexpectedEnd)
		}
		if rest[0] == 'e' {
			return dict, rest[1:], nil
		}

		// Key must be a string
		if rest[0] < '0' || rest[0] > '9' {
			return nil, nil, fmt.Errorf("%w: dict key must be a string", ErrInvalidFormat)
		}
		key, remaining, err := decodeString(rest)
		if err != nil {
			return nil, nil, err
		}

		// Keys must be sorted (allow duplicates — last value wins, like libtorrent)
		if len(dict) > 0 && key < prevKey {
			return nil, nil, fmt.Errorf("%w: dict keys not sorted: %q after %q", ErrInvalidFormat, key, prevKey)
		}
		prevKey = key

		// Value
		if len(remaining) == 0 {
			return nil, nil, fmt.Errorf("%w: missing value for key %q", ErrUnexpectedEnd, key)
		}
		val, remaining2, err := decodeValue(remaining)
		if err != nil {
			return nil, nil, err
		}

		dict[key] = val
		rest = remaining2
	}
}
