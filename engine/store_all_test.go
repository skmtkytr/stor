package engine

import "testing"

// TestStoreAllReturnsCopies verifies that All() returns copies, not
// pointers to the internal map entries. Mutating a returned record
// must not affect the store's internal state.
func TestStoreAllReturnsCopies(t *testing.T) {
	s := NewStore("")
	s.Put(&TorrentRecord{ID: "a", Name: "original"})

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All: got %d, want 1", len(all))
	}

	// Mutate the returned record
	all[0].Name = "mutated"

	// The store's internal record must be unchanged
	r, ok := s.Get("a")
	if !ok {
		t.Fatal("record not found")
	}
	if r.Name != "original" {
		t.Errorf("Store internal state was mutated via All(): Name=%q, want %q", r.Name, "original")
	}
}
