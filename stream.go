package claudecli

import (
	"context"
	"errors"
	"sync"
)

// State represents the lifecycle state of a Stream.
type State int

const (
	StateStarting State = iota
	StateIdle
	StateRunning
	StateDone
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateDone:
		return "done"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// ErrEmptyOutput is returned when RunText/RunJSON receive no text output.
var ErrEmptyOutput = errors.New("claudecli: empty output")

// Stream provides access to events from a Claude CLI session.
// Use Events() for channel-based iteration, Next() for pull-based,
// or Wait() to block until completion.
type Stream struct {
	events <-chan Event
	done   <-chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	state  State
	result *ResultEvent
	err    error
	waited bool
}

func newStream(ctx context.Context, raw <-chan Event, done <-chan struct{}, cancel context.CancelFunc) *Stream {
	tracked := make(chan Event, 64)
	s := &Stream{
		events: tracked,
		done:   done,
		cancel: cancel,
		state:  StateStarting,
	}

	// Interpose: track state on every event regardless of consumption method
	go func() {
		defer close(tracked)
		for event := range raw {
			s.trackState(event)
			select {
			case tracked <- event:
			case <-ctx.Done():
				for range raw {
				}
				return
			}
		}
	}()

	return s
}

// Events returns a channel of events. The channel is closed when the
// stream ends. Safe for range iteration. State is tracked automatically.
func (s *Stream) Events() <-chan Event {
	return s.events
}

// Next returns the next event and true, or zero value and false when done.
func (s *Stream) Next() (Event, bool) {
	event, ok := <-s.events
	return event, ok
}

// State returns the current lifecycle state.
func (s *Stream) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Wait drains all remaining events and returns the final result.
// Idempotent: multiple calls return the same result.
func (s *Stream) Wait() (*ResultEvent, error) {
	s.mu.Lock()
	if s.waited {
		result, err := s.result, s.err
		s.mu.Unlock()
		return result, err
	}
	s.mu.Unlock()

	for range s.events {
	}

	<-s.done

	s.mu.Lock()
	defer s.mu.Unlock()
	s.waited = true
	return s.result, s.err
}

// Close cancels the underlying process and waits for cleanup.
func (s *Stream) Close() error {
	s.cancel()
	for range s.events {
	}
	<-s.done
	return nil
}

func (s *Stream) trackState(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch e := event.(type) {
	case *InitEvent:
		s.state = StateRunning
	case *ResultEvent:
		s.state = StateDone
		s.result = e
	case *ErrorEvent:
		if e.Fatal {
			s.state = StateFailed
			if s.err == nil {
				s.err = e.Err
			}
		}
	}
}
