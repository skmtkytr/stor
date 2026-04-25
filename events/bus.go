package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// SubscribeOptions configures a new subscription.
type SubscribeOptions struct {
	// Buffer is the per-subscriber channel capacity. Default 256.
	Buffer int
	// Filter, when non-nil, drops events for which it returns false
	// before they would consume buffer space.
	Filter func(Event) bool
	// Name is an opaque label used for metrics / logging only.
	Name string
}

// Subscription is the consumer-side handle returned by Bus.Subscribe.
type Subscription struct {
	C chan Event // receive-only consumer view; closed when ctx ends or Bus closes

	filter  func(Event) bool
	name    string
	dropped atomic.Int64
}

// Name returns the label this subscription was created with.
func (s *Subscription) Name() string { return s.name }

// Dropped returns the number of events dropped because the channel was full.
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

// Bus is a non-blocking fan-out publisher.
type Bus struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool
	onPub  func(Event)
	onDrop func(*Subscription, Event)
}

// New returns a fresh Bus.
func New() *Bus {
	return &Bus{subs: make(map[*Subscription]struct{})}
}

// SetHooks installs optional observation hooks (for metrics). Hooks are
// invoked without locks; they must not call back into the Bus.
func (b *Bus) SetHooks(onPublish func(Event), onDrop func(*Subscription, Event)) {
	b.mu.Lock()
	b.onPub = onPublish
	b.onDrop = onDrop
	b.mu.Unlock()
}

// Publish emits an event to all matching subscribers. Never blocks: if a
// subscriber's buffer is full, the event is dropped for that subscriber and
// its drop counter is incremented.
func (b *Bus) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	pub := b.onPub
	drop := b.onDrop
	subs := make([]*Subscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.RUnlock()

	if pub != nil {
		pub(ev)
	}
	for _, sub := range subs {
		if sub.filter != nil && !sub.filter(ev) {
			continue
		}
		select {
		case sub.C <- ev:
		default:
			sub.dropped.Add(1)
			if drop != nil {
				drop(sub, ev)
			}
		}
	}
}

// Subscribe returns a Subscription whose C channel receives events until
// ctx is cancelled or the Bus is closed.
func (b *Bus) Subscribe(ctx context.Context, opts SubscribeOptions) *Subscription {
	buf := opts.Buffer
	if buf <= 0 {
		buf = 256
	}
	sub := &Subscription{
		C:      make(chan Event, buf),
		filter: opts.Filter,
		name:   opts.Name,
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(sub.C)
		return sub
	}
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		if _, ok := b.subs[sub]; ok {
			delete(b.subs, sub)
			close(sub.C)
		}
		b.mu.Unlock()
	}()

	return sub
}

// Close stops the bus and closes every active subscription channel.
// Safe to call multiple times. Subsequent Publish calls become no-ops.
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for sub := range b.subs {
		close(sub.C)
	}
	b.subs = nil
	b.mu.Unlock()
}
