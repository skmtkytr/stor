package bencode

import (
	"errors"
	"testing"
)

func TestDecodeNestedCollectionTotalLimit(t *testing.T) {
	// A list of 100 dicts, each with 50000 items = 5,000,000 total items.
	// Each individual collection is under MaxCollectionSize (1M), but the total
	// allocation is large. MaxTotalItems should limit cumulative allocation.
	//
	// Build: l d <50000 entries> e d <50000 entries> e ... (100 dicts) e
	var buf []byte
	buf = append(buf, 'l')
	for range 100 {
		buf = append(buf, 'd')
		for j := range 50000 {
			// Key: 5-char zero-padded
			key := [5]byte{
				byte('0' + (j/10000)%10),
				byte('0' + (j/1000)%10),
				byte('0' + (j/100)%10),
				byte('0' + (j/10)%10),
				byte('0' + j%10),
			}
			buf = append(buf, "5:"...)
			buf = append(buf, key[:]...)
			buf = append(buf, "i0e"...)
		}
		buf = append(buf, 'e')
	}
	buf = append(buf, 'e')

	_, err := Decode(buf)
	if err == nil {
		t.Fatal("expected error for nested collections exceeding total item limit")
	}
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidFormat, got: %v", err)
	}
}
