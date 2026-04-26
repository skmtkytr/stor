package events

import (
	"context"
	"sync"
)

// Recorder is a test helper that drains a subscription into a slice.
//
//	rec := events.NewRecorder()
//	go rec.Drain(bus.Subscribe(ctx, events.SubscribeOptions{}))
//	... // do work that publishes events
//	cancel()        // close subscription
//	rec.Wait()      // wait for the drain goroutine to finish
//	got := rec.Events()
type Recorder struct {
	mu     sync.Mutex
	events []Event
	done   chan struct{}
}

// NewRecorder returns a fresh Recorder.
func NewRecorder() *Recorder {
	return &Recorder{done: make(chan struct{})}
}

// Drain consumes from sub.C until sub.Done() fires, appending each event.
// Any events still buffered in sub.C when Done fires are also drained.
// Run this in a goroutine.
func (r *Recorder) Drain(sub *Subscription) {
	defer close(r.done)
	for {
		select {
		case ev := <-sub.C:
			r.appendEvent(ev)
		case <-sub.Done():
			// Drain anything still buffered, then exit.
			for {
				select {
				case ev := <-sub.C:
					r.appendEvent(ev)
				default:
					return
				}
			}
		}
	}
}

func (r *Recorder) appendEvent(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

// Events returns a copy of the recorded events so far.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Wait blocks until Drain returns (i.e. the subscription channel was closed).
func (r *Recorder) Wait() { <-r.done }

// WaitContext is Wait with a cancellation escape.
func (r *Recorder) WaitContext(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
