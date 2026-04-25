package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBusFanout(t *testing.T) {
	b := New()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s1 := b.Subscribe(ctx, SubscribeOptions{Buffer: 4, Name: "s1"})
	s2 := b.Subscribe(ctx, SubscribeOptions{Buffer: 4, Name: "s2"})

	b.Publish(Event{Type: TypeTorrentAdded, TorrentID: "abc"})

	for _, sub := range []*Subscription{s1, s2} {
		select {
		case ev := <-sub.C:
			if ev.Type != TypeTorrentAdded {
				t.Errorf("subscriber %q: got %v, want %v", sub.Name(), ev.Type, TypeTorrentAdded)
			}
			if ev.TorrentID != "abc" {
				t.Errorf("subscriber %q: torrent id = %q, want abc", sub.Name(), ev.TorrentID)
			}
			if ev.Time.IsZero() {
				t.Errorf("subscriber %q: time was not stamped", sub.Name())
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %q did not receive event", sub.Name())
		}
	}
}

func TestBusFilter(t *testing.T) {
	b := New()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := b.Subscribe(ctx, SubscribeOptions{
		Buffer: 4,
		Filter: func(e Event) bool { return e.Type == TypePieceComplete },
	})

	b.Publish(Event{Type: TypeTorrentAdded})
	b.Publish(Event{Type: TypePieceComplete})
	b.Publish(Event{Type: TypeAnnounceReply})

	select {
	case ev := <-sub.C:
		if ev.Type != TypePieceComplete {
			t.Errorf("got %v, want %v", ev.Type, TypePieceComplete)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive filtered event")
	}

	select {
	case ev := <-sub.C:
		t.Errorf("unexpected event leaked through filter: %v", ev.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBusDropOnFull(t *testing.T) {
	b := New()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := b.Subscribe(ctx, SubscribeOptions{Buffer: 2})

	for range 10 {
		b.Publish(Event{Type: TypePieceComplete})
	}

	if got := sub.Dropped(); got < 8 {
		t.Errorf("expected at least 8 drops, got %d", got)
	}
}

func TestBusCtxCancelUnsubscribes(t *testing.T) {
	b := New()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())

	sub := b.Subscribe(ctx, SubscribeOptions{Buffer: 1})
	cancel()

	select {
	case _, ok := <-sub.C:
		if ok {
			t.Error("expected channel to be closed after ctx cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel never closed after ctx cancel")
	}

	// Publish must not panic with a stale subscription gone.
	b.Publish(Event{Type: TypeTorrentAdded})
}

func TestBusCloseUnblocksSubscribers(t *testing.T) {
	b := New()
	ctx := context.Background()
	sub := b.Subscribe(ctx, SubscribeOptions{Buffer: 1})

	b.Close()

	select {
	case _, ok := <-sub.C:
		if ok {
			t.Error("expected channel to be closed by Bus.Close")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after Bus.Close")
	}

	// Idempotent close.
	b.Close()
	// Publish after close is a no-op.
	b.Publish(Event{Type: TypeTorrentAdded})
}

func TestBusSubscribeAfterClose(t *testing.T) {
	b := New()
	b.Close()
	ctx := context.Background()
	sub := b.Subscribe(ctx, SubscribeOptions{Buffer: 1})
	if _, ok := <-sub.C; ok {
		t.Error("expected immediately-closed channel for post-close Subscribe")
	}
}

func TestBusConcurrentPublish(t *testing.T) {
	b := New()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := b.Subscribe(ctx, SubscribeOptions{Buffer: 2000})

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				b.Publish(Event{Type: TypePieceComplete})
			}
		}()
	}
	wg.Wait()

	count := 0
	timeout := time.After(2 * time.Second)
	for count < 1000 {
		select {
		case _, ok := <-sub.C:
			if !ok {
				t.Fatalf("channel closed early at count=%d", count)
			}
			count++
		case <-timeout:
			t.Fatalf("got %d events, want 1000 (drops=%d)", count, sub.Dropped())
		}
	}
}

func TestBusHooks(t *testing.T) {
	b := New()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var pubCount, dropCount atomic.Int64
	b.SetHooks(
		func(_ Event) { pubCount.Add(1) },
		func(_ *Subscription, _ Event) { dropCount.Add(1) },
	)

	_ = b.Subscribe(ctx, SubscribeOptions{Buffer: 1})

	for range 5 {
		b.Publish(Event{Type: TypeTorrentAdded})
	}

	if got := pubCount.Load(); got != 5 {
		t.Errorf("publish hook called %d times, want 5", got)
	}
	if got := dropCount.Load(); got < 3 {
		t.Errorf("drop hook called %d times, want >= 3", got)
	}
}

func TestRecorder(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())

	rec := NewRecorder()
	sub := b.Subscribe(ctx, SubscribeOptions{Buffer: 16})
	go rec.Drain(sub)

	b.Publish(Event{Type: TypeTorrentAdded, TorrentID: "x"})
	b.Publish(Event{Type: TypeStateChanged, TorrentID: "x"})

	// Give Publish a moment to deliver before we cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()
	rec.Wait()
	b.Close()

	got := rec.Events()
	if len(got) != 2 {
		t.Fatalf("recorded %d events, want 2", len(got))
	}
	if got[0].Type != TypeTorrentAdded || got[1].Type != TypeStateChanged {
		t.Errorf("unexpected event order: %+v", got)
	}
}
