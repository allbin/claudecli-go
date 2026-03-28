package claudecli

import (
	"context"
	"fmt"
	"sync"
)

// SessionMeta holds consumer-defined metadata about a session in a Pool.
type SessionMeta struct {
	Name   string
	Labels map[string]string
}

// SessionEntry pairs a Session with its metadata.
type SessionEntry struct {
	Session *Session
	Meta    SessionMeta
}

// PoolEvent wraps an Event with the ID of the session that produced it.
type PoolEvent struct {
	SessionID string
	Event     Event
}

// Pool is a multi-session registry that multiplexes events from registered
// Sessions into a single channel. Thread-safe for concurrent Add/Remove/Get/List.
type Pool struct {
	mu       sync.RWMutex
	sessions map[string]*poolEntry
	events   chan PoolEvent
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
}

type poolEntry struct {
	session *Session
	meta    SessionMeta
	cancel  context.CancelFunc
}

// NewPool creates a Pool with a buffered event channel (capacity 256).
func NewPool() *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{
		sessions: make(map[string]*poolEntry),
		events:   make(chan PoolEvent, 256),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Add registers a session with metadata. The pool immediately starts forwarding
// events from this session to the multiplexed channel.
// The session must have a SessionID (i.e., its InitEvent must have been received).
func (p *Pool) Add(session *Session, meta SessionMeta) error {
	id := session.SessionID()
	if id == "" {
		return fmt.Errorf("session has no ID")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return fmt.Errorf("pool is closed")
	}
	if _, exists := p.sessions[id]; exists {
		return fmt.Errorf("session %q already registered", id)
	}

	ctx, cancel := context.WithCancel(p.ctx)
	p.sessions[id] = &poolEntry{
		session: session,
		meta:    meta,
		cancel:  cancel,
	}

	p.wg.Add(1)
	go p.forward(ctx, id, session.Events())

	return nil
}

// Remove unregisters a session. Does not close the session itself.
func (p *Pool) Remove(sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %q not found", sessionID)
	}

	entry.cancel()
	delete(p.sessions, sessionID)
	return nil
}

// Get returns a session and its metadata by ID.
func (p *Pool) Get(sessionID string) (*Session, SessionMeta, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	entry, ok := p.sessions[sessionID]
	if !ok {
		return nil, SessionMeta{}, false
	}
	return entry.session, entry.meta, true
}

// List returns all registered sessions with metadata.
func (p *Pool) List() []SessionEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	entries := make([]SessionEntry, 0, len(p.sessions))
	for _, e := range p.sessions {
		entries = append(entries, SessionEntry{
			Session: e.session,
			Meta:    e.meta,
		})
	}
	return entries
}

// Events returns a channel of tagged events from all registered sessions.
func (p *Pool) Events() <-chan PoolEvent {
	return p.events
}

// Close stops all forwarders and closes the events channel.
// Does not close individual sessions. Idempotent.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	p.cancel()
	p.wg.Wait()
	close(p.events)
}

func (p *Pool) forward(ctx context.Context, sessionID string, ch <-chan Event) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			select {
			case p.events <- PoolEvent{SessionID: sessionID, Event: ev}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// FormatAgentMessage formats a message as coming from another agent session.
// The formatted string is suitable for injection via Session.SendMessage().
func FormatAgentMessage(senderName, content string) string {
	return fmt.Sprintf("[Message from agent %q]\n%s\n[End of agent message]", senderName, content)
}

// SendAgentMessage formats and sends an inter-agent message between two
// sessions registered in the pool. Uses the sender's SessionMeta.Name.
func (p *Pool) SendAgentMessage(fromSessionID, toSessionID, content string) error {
	p.mu.RLock()
	from, fromOK := p.sessions[fromSessionID]
	to, toOK := p.sessions[toSessionID]
	p.mu.RUnlock()

	if !fromOK {
		return fmt.Errorf("sender session %q not found", fromSessionID)
	}
	if !toOK {
		return fmt.Errorf("target session %q not found", toSessionID)
	}

	msg := FormatAgentMessage(from.meta.Name, content)
	return to.session.SendMessage(msg)
}
