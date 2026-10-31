package events

import (
	"errors"
	"sync"
)

// ErrTooManySubscribers is returned when the subscriber limit is reached.
var ErrTooManySubscribers = errors.New("too many event subscribers")

// Hub fans events out to the subscribers of this process. Publishing never
// blocks: a subscriber whose buffer is full is disconnected instead of
// slowing down everyone else. Disconnected clients reconnect and simply miss
// the events sent meanwhile, which matches the at-most-once delivery
// promised by the API.
type Hub struct {
	mu             sync.Mutex
	subs           map[*Subscription]struct{}
	buffer         int
	maxSubscribers int
	closed         bool
	observer       Observer
}

// Observer receives hub activity, typically to record metrics.
type Observer interface {
	SubscriberAdded()
	SubscriberRemoved()
	SubscriberDropped()
}

type noopObserver struct{}

func (noopObserver) SubscriberAdded()   {}
func (noopObserver) SubscriberRemoved() {}
func (noopObserver) SubscriberDropped() {}

// Subscription is one subscriber's stream of events. C is closed when the
// subscription ends: unsubscribed, dropped for being too slow, or hub closed.
type Subscription struct {
	C       <-chan StateChanged
	ch      chan StateChanged
	dropped bool
}

// Dropped reports whether the subscription ended because the subscriber
// could not keep up. Only meaningful after C is closed.
func (s *Subscription) Dropped() bool {
	return s.dropped
}

// NewHub returns a hub that buffers up to buffer events per subscriber and
// accepts up to maxSubscribers subscribers (0 means no limit).
func NewHub(buffer, maxSubscribers int, observer Observer) *Hub {
	if buffer < 1 {
		buffer = 1
	}
	if observer == nil {
		observer = noopObserver{}
	}
	return &Hub{
		subs:           make(map[*Subscription]struct{}),
		buffer:         buffer,
		maxSubscribers: maxSubscribers,
		observer:       observer,
	}
}

// Subscribe registers a new subscriber.
func (h *Hub) Subscribe() (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("event hub is closed")
	}
	if h.maxSubscribers > 0 && len(h.subs) >= h.maxSubscribers {
		return nil, ErrTooManySubscribers
	}
	ch := make(chan StateChanged, h.buffer)
	s := &Subscription{C: ch, ch: ch}
	h.subs[s] = struct{}{}
	h.observer.SubscriberAdded()
	return s, nil
}

// Unsubscribe ends s. It is safe to call more than once and after the
// subscription was dropped.
func (h *Hub) Unsubscribe(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(s)
}

// Publish delivers e to every subscriber without blocking.
func (h *Hub) Publish(e StateChanged) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.ch <- e:
		default:
			s.dropped = true
			h.observer.SubscriberDropped()
			h.remove(s)
		}
	}
}

// Subscribers returns the number of active subscribers.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close ends every subscription and rejects new ones. It is used on
// shutdown so open streams finish and the server can drain.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		h.remove(s)
	}
}

// remove must be called with h.mu held.
func (h *Hub) remove(s *Subscription) {
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	close(s.ch)
	h.observer.SubscriberRemoved()
}
