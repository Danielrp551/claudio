package daemon

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Loopback is a transport that never touches the network.
//
// It exists for the milestone that proves the hardest part of this project
// before any of the easy parts are built: a remote session appears in the local
// agent list, the local Claude writes to it, and an answer comes back into the
// same conversation. Everything in that sentence is real except the network.
//
// It is also what the daemon tests run against, because a test that needed a
// relay would be testing the relay.
type Loopback struct {
	roster []RemoteSession

	// Answer turns an outgoing message into the reply that comes back. A nil
	// Answer means nothing comes back, which is the right shape for a test that
	// only cares about the outbound half.
	Answer func(Outbound) string

	deliveries chan Delivery

	closeOnce sync.Once
	closed    chan struct{}
}

// NewLoopback returns a transport that reports roster as reachable.
func NewLoopback(roster []RemoteSession, answer func(Outbound) string) *Loopback {
	return &Loopback{
		roster:     roster,
		Answer:     answer,
		deliveries: make(chan Delivery, 64),
		closed:     make(chan struct{}),
	}
}

// Send accepts a message and, when an answer function is set, turns it into a
// delivery going the other way.
func (l *Loopback) Send(ctx context.Context, msg Outbound) error {
	select {
	case <-l.closed:
		return errors.New("daemon: the loopback transport is closed")
	default:
	}

	if l.Answer == nil {
		return nil
	}
	text := l.Answer(msg)
	if text == "" {
		return nil
	}

	from := RemoteSession{ID: msg.To}
	for _, s := range l.roster {
		if s.ID == msg.To {
			from = s
			break
		}
	}

	reply := Delivery{
		From:      from,
		ToSession: msg.FromSession,
		Text:      text,
		FromMode:  "prompting",
		MsgID:     msg.MsgID + "-reply",
	}

	select {
	case l.deliveries <- reply:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-l.closed:
		return errors.New("daemon: the loopback transport is closed")
	case <-time.After(5 * time.Second):
		return errors.New("daemon: nobody is reading deliveries")
	}
}

// Deliveries yields the answers this transport produces.
func (l *Loopback) Deliveries() <-chan Delivery { return l.deliveries }

// Roster returns the remote sessions this transport pretends to reach.
func (l *Loopback) Roster() []RemoteSession {
	out := make([]RemoteSession, len(l.roster))
	copy(out, l.roster)
	return out
}

// Close stops the transport. It is safe to call more than once.
func (l *Loopback) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		close(l.deliveries)
	})
	return nil
}
