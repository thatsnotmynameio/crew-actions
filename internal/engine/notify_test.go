package engine

import "testing"

// isClosed reports whether ch is closed, without waiting.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestBroadcastWakesAWaiterSubscribedBefore(t *testing.T) {
	n := newNotifier()
	first, second := n.subscribe(), n.subscribe()
	if isClosed(first) {
		t.Fatal("a subscription is closed before any broadcast")
	}
	// The broadcast comes before either waiter starts waiting.
	n.broadcast()
	if !isClosed(first) || !isClosed(second) {
		t.Error("a waiter subscribed before the broadcast was not woken")
	}
}

func TestBroadcastDoesNotWakeALaterSubscription(t *testing.T) {
	n := newNotifier()
	n.broadcast()
	later := n.subscribe()
	if isClosed(later) {
		t.Fatal("a subscription made after a broadcast is closed by it")
	}
	n.broadcast()
	if !isClosed(later) {
		t.Error("the next broadcast did not wake the later subscription")
	}
}
