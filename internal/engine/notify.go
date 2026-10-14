package engine

import "sync"

// notifier wakes everything that waits for the engine's next write. A
// waiter subscribes before it reads, so a write between its read and its
// wait still wakes it (KTD6).
type notifier struct {
	mu sync.Mutex
	// changed is closed by the next broadcast.
	changed chan struct{}
}

// newNotifier returns a notifier with no broadcast pending.
func newNotifier() *notifier {
	return &notifier{changed: make(chan struct{})}
}

// subscribe returns a channel the next broadcast closes.
func (n *notifier) subscribe() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.changed
}

// broadcast wakes every subscription made before it.
func (n *notifier) broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.changed)
	n.changed = make(chan struct{})
}
