package bencode

import (
	"fmt"
	"sort"
	"strconv"
)

// Encode encodes a Go value into bencode format.
// Accepted types: string, []byte, int (any width), []any, map[string]any.
func Encode(v any) ([]byte, error) {
	var buf []byte
	var err error
	buf, err = encodeValue(buf, v)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func encodeValue(buf []byte, v any) ([]byte, error) {
	switch val := v.(type) {
	case string:
		return encodeString(buf, val), nil
	case []byte:
		return encodeBytes(buf, val), nil
	case int64:
		return encodeInt(buf, val), nil
	case int:
		return encodeInt(buf, int64(val)), nil
	case []any:
		return encodeList(buf, val)
	case map[string]any:
		return encodeDict(buf, val)
	default:
		return nil, fmt.Errorf("bencode: unsupported type %T", v)
	}
}

func encodeString(buf []byte, s string) []byte {
	buf = strconv.AppendInt(buf, int64(len(s)), 10)
	buf = append(buf, ':')
	buf = append(buf, s...)
	return buf
}

func encodeBytes(buf []byte, b []byte) []byte {
	buf = strconv.AppendInt(buf, int64(len(b)), 10)
	buf = append(buf, ':')
	buf = append(buf, b...)
	return buf
}

func encodeInt(buf []byte, n int64) []byte {
	buf = append(buf, 'i')
	buf = strconv.AppendInt(buf, n, 10)
	buf = append(buf, 'e')
	return buf
}

func encodeList(buf []byte, l []any) ([]byte, error) {
	buf = append(buf, 'l')
	for _, v := range l {
		var err error
		buf, err = encodeValue(buf, v)
		if err != nil {
			return nil, err
		}
	}
	buf = append(buf, 'e')
	return buf, nil
}

func encodeDict(buf []byte, d map[string]any) ([]byte, error) {
	// Keys must be sorted
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf = append(buf, 'd')
	for _, k := range keys {
		buf = encodeString(buf, k)
		var err error
		buf, err = encodeValue(buf, d[k])
		if err != nil {
			return nil, err
		}
	}
	buf = append(buf, 'e')
	return buf, nil
}
