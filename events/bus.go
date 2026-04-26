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
//
// C is the event channel. It is NEVER closed — that would race with
// Publish and panic. To know when the subscription has ended (ctx cancelled
// or Bus closed), select on Done() in addition to C:
//
//	for {
//		select {
//		case ev := <-sub.C:
//			// handle ev
//		case <-sub.Done():
//			return
//		}
//	}
//
// Events still buffered in C when Done() fires can optionally be drained
// with a non-blocking receive loop before exiting.
type Subscription struct {
	C chan Event

	done      chan struct{}
	closeOnce sync.Once

	filter  func(Event) bool
	name    string
	dropped atomic.Int64
}

// Name returns the label this subscription was created with.
func (s *Subscription) Name() string { return s.name }

// Dropped returns the number of events dropped because the channel was full.
func (s *Subscription) Dropped() int64 { return s.dropped.Load() }

// Done returns a channel closed when the subscription has ended.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// close marks the subscription ended. Safe to call multiple times.
func (s *Subscription) close() {
	s.closeOnce.Do(func() { close(s.done) })
}

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
// its drop counter is incremented. If a subscription has already ended
// (Done closed) by the time we try to deliver, the event is silently
// skipped without counting as a drop — the consumer is gone.
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
			// delivered
		case <-sub.done:
			// subscription ended between snapshot and send; skip
		default:
			sub.dropped.Add(1)
			if drop != nil {
				drop(sub, ev)
			}
		}
	}
}

// Subscribe returns a Subscription whose C channel receives events until
// ctx is cancelled or the Bus is closed. Done() fires on either condition.
// The C channel is never closed (closing it would race with Publish).
func (b *Bus) Subscribe(ctx context.Context, opts SubscribeOptions) *Subscription {
	buf := opts.Buffer
	if buf <= 0 {
		buf = 256
	}
	sub := &Subscription{
		C:      make(chan Event, buf),
		done:   make(chan struct{}),
		filter: opts.Filter,
		name:   opts.Name,
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		sub.close()
		return sub
	}
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
		sub.close()
	}()

	return sub
}

// Close stops the bus and signals every active subscription to end via
// Done(). Safe to call multiple times. Subsequent Publish calls become
// no-ops. Subscription.C is intentionally NOT closed: doing so would race
// with in-flight Publish goroutines and panic.
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = nil
	b.mu.Unlock()

	for sub := range subs {
		sub.close()
	}
}
