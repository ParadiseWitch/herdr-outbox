package api

import (
	"fmt"
	"sync"
)

// Broadcaster fans out events to multiple subscribers. Each subscriber gets a
// buffered channel; a slow subscriber drops events rather than blocking the publisher.
type Broadcaster struct {
	mu      sync.Mutex
	subs    map[uint64]chan Event
	nextID  uint64
	bufSize int
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subs:    make(map[uint64]chan Event),
		bufSize: 64,
	}
}

// Subscribe returns a channel that receives events and an ID for Unsubscribe.
func (b *Broadcaster) Subscribe() (<-chan Event, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan Event, b.bufSize)
	b.subs[id] = ch
	return ch, id
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Broadcaster) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		close(ch)
		delete(b.subs, id)
	}
}

// Publish sends an event to all subscribers. Slow subscribers have the event dropped.
func (b *Broadcaster) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// FormatSSE formats an event as a Server-Sent Events data line.
func FormatSSE(ev Event) string {
	return fmt.Sprintf("data: %s\n\n", ev.Kind)
}

// SubscriberCount returns the number of active subscribers.
func (b *Broadcaster) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
