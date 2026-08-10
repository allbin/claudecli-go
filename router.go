package claudecli

// Query↔svar-korrelation (rotorsaksfix 2026-08-10).
//
// Problemet: Session producerar events sessions-skopat (en gemensam
// Events()-kanal) medan konsumenter typiskt läser query-skopat (en eventloop
// per fråga). Ett färdigt ResultEvent som anländer när ingen lyssnar buffras
// i kanalen och flushas som svar på NÄSTA fråga — off-by-one-svaret.
//
// Fixen: en kontinuerlig, sessions-ägd konsument. Pumpen i readLoop dränerar
// ALLTID den interna event-strömmen; varje event ankomststämplas vid enqueue
// (Session.stamp: tidpunkt + aktiv query-generation) och routas till
//
//	(a) den aktiva queryns mailbox (QueryHandle) när generationerna matchar,
//	(b) orphan-mailboxen annars (ingen aktiv query, eller detachad query).
//
// Ett buffrat äldre ResultEvent kan därmed aldrig läcka in i fel query, och
// sena färdiga svar från avbrutna queries kan hämtas via DrainOrphans och
// levereras separat i stället för att kastas.
//
// Backpressure: routern blockerar ALDRIG pumpen. Båda mailboxarna är bounded
// med drop-oldest + räknare — en långsam eller försvunnen konsument kan inte
// proppa igen pump → readLoop → childens stdout-pipe (rotorsak B).
//
// Aktiveras opt-in via EnableRouting()/första QueryCtx(). Sessioner som inte
// aktiverar routing behåller exakt den gamla Events()-semantiken — API:t är
// additivt och voice-agenterna påverkas inte.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrQueryDetached is returned by QueryHandle.Wait when the handle was
// detached (Detach() or a newer QueryCtx superseded it) before its result
// arrived. The late result, if any, surfaces via Session.DrainOrphans.
var ErrQueryDetached = errors.New("query detached")

const (
	// orphanMailboxCap bounds the orphan mailbox. Äldsta posten kastas först
	// (räknas i RouterStats.OrphansDropped) så routern aldrig blockerar.
	orphanMailboxCap = 128
	// queryMailboxCap bounds each query's mailbox (drop-oldest). Terminala
	// events överlever i praktiken alltid eftersom de anländer sist.
	queryMailboxCap = 256
)

// stampedEvent is the internal arrival-stamped envelope that flows through
// the pump. Implements Event so the untyped pump channel can carry it; it is
// always unwrapped before delivery and never reaches consumers.
type stampedEvent struct {
	ev  Event
	at  time.Time
	gen uint64 // aktiv query-generation vid ankomst; 0 = ingen
}

func (*stampedEvent) event() {}

// routerBarrierEvent flushes events that were buffered in s.events before
// routing was enabled into the orphan mailbox. Internal; never delivered.
type routerBarrierEvent struct{}

func (*routerBarrierEvent) event() {}

// OrphanEvent is an event that arrived outside any active query — before the
// first query, between queries, or after its query's handle was detached —
// together with its arrival stamp.
type OrphanEvent struct {
	Event     Event
	ArrivedAt time.Time
	// ActiveQueryAtArrival is the query generation that was active when the
	// event arrived; 0 when none. Events from an abandoned (detached) query
	// keep that query's generation — match against QueryHandle.Gen() to
	// correlate a late result back to its original question.
	ActiveQueryAtArrival uint64
}

// RouterStats reports drop counters for the bounded router mailboxes.
type RouterStats struct {
	// OrphansDropped counts oldest-first drops from the orphan mailbox.
	OrphansDropped uint64
	// MailboxDropped counts oldest-first drops across all query mailboxes.
	MailboxDropped uint64
}

// QueryHandle is one query's private event stream. Created by QueryCtx.
// Events() yields only events stamped for this query's generation, so a
// buffered older ResultEvent can never leak in.
type QueryHandle struct {
	gen uint64
	s   *Session
	ch  chan Event

	dropped atomic.Uint64

	// guarded by the session router's mu:
	detached    bool
	closeReason error // safe to read after ch closes (happens-before via close)
}

// Events returns this query's private event channel. Closed when the session
// ends, when Detach() is called, or when a newer QueryCtx supersedes the
// handle. Efter stängning kan redan buffrade events fortfarande dräneras —
// range avslutas först när bufferten är tom.
func (h *QueryHandle) Events() <-chan Event { return h.ch }

// Gen returns the query generation this handle correlates to.
func (h *QueryHandle) Gen() uint64 { return h.gen }

// Dropped returns how many events were dropped from this handle's bounded
// mailbox (drop-oldest under backpressure). Normally 0.
func (h *QueryHandle) Dropped() uint64 { return h.dropped.Load() }

// Detach stops delivery to this handle and closes its channel. Anropa när en
// query överges (supersede/interrupt): events som redan ligger i mailboxen
// kan fortfarande dräneras ur den stängda kanalen, och events som anländer
// SENARE för samma query (t.ex. ett sent färdigt ResultEvent efter Interrupt)
// hamnar i orphan-mailboxen med ActiveQueryAtArrival == Gen() — hämta dem med
// Session.DrainOrphans och leverera separat. Idempotent.
func (h *QueryHandle) Detach() {
	h.s.router.detach(h, ErrQueryDetached)
}

// Wait consumes events from the handle until this query's terminal event and
// returns the result. Terminal events: ResultEvent (returned), fatal
// ErrorEvent (error), CLIExitEvent (error — child died before the result).
// Non-terminal events are discarded — use either Events() or Wait(), not
// both. Returns ctx.Err() when ctx expires; the query keeps running (use
// Interrupt/Detach to abandon it).
func (h *QueryHandle) Wait(ctx context.Context) (*ResultEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case ev, ok := <-h.ch:
			if !ok {
				if h.closeReason != nil {
					return nil, h.closeReason
				}
				return nil, errSessionEnded
			}
			switch e := ev.(type) {
			case *ResultEvent:
				return e, nil
			case *ErrorEvent:
				if e.Fatal {
					return nil, e.Err
				}
			case *CLIExitEvent:
				if e.Err != nil {
					return nil, fmt.Errorf("cli exited before result: %w", e.Err)
				}
				return nil, fmt.Errorf("cli exited before result (%s)", e.Reason)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// eventRouter owns the query mailbox routing state. Skapad av EnableRouting;
// alla fält skyddas av mu. Leverans sker på pumpgoroutinen (plus readLoops
// defer för det avslutande CLIExitEvent, när pumpen redan är död).
type eventRouter struct {
	mu     sync.Mutex
	active *QueryHandle

	// orphan-mailbox: fast ring med drop-oldest.
	orphans        []OrphanEvent
	head           int
	count          int
	orphansDropped uint64
	mailboxDropped uint64

	shutdown bool
}

func newEventRouter() *eventRouter {
	return &eventRouter{orphans: make([]OrphanEvent, orphanMailboxCap)}
}

// EnableRouting switches the session into routed event delivery. Efter detta
// levereras inga events på Session.Events() — konsumera via QueryCtx-handtag
// och DrainOrphans i stället (blanda inte med direkt Events()-läsning).
// Events som redan hunnit bufferas i Events()-kanalen flushas till
// orphan-mailboxen (deras ankomsttid är okänd; flush-tidpunkten används).
// Idempotent; anropas implicit av första QueryCtx.
func (s *Session) EnableRouting() {
	s.routerOnce.Do(func() {
		s.router = newEventRouter()
		s.routed.Store(true)
		if s.routedCh != nil {
			// Väck en pump som står blockerad mot en okonsumerad s.events —
			// utan detta skulle barriären nedan aldrig nå fram (pumpen står
			// still) och enable-vägen deadlocka.
			close(s.routedCh)
		}
		// Barriär genom pumpen: när pumpgoroutinen (enda skrivaren av
		// s.events) processar den är den per definition inte mitt i en
		// s.events-skrivning, så flushen av kvarvarande buffrade events
		// kappkör inte med någon.
		s.sendEvent(&routerBarrierEvent{})
	})
}

// routeStamped delivers an arrival-stamped event to the active query's
// mailbox or the orphan mailbox. Körs på pumpgoroutinen (och på readLoops
// defer för exit-eventet). Blockerar aldrig: bounded mailboxar, drop-oldest.
func (s *Session) routeStamped(sev *stampedEvent) {
	if _, isBarrier := sev.ev.(*routerBarrierEvent); isBarrier {
		s.flushLegacyBuffered()
		return
	}
	r := s.router
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shutdown {
		return
	}
	h := r.active
	if h != nil && !h.detached && sev.gen == h.gen {
		for {
			select {
			case h.ch <- sev.ev:
				return
			default:
			}
			// Fullt: kasta äldsta och försök igen. Enda sändaren håller
			// r.mu, så loopen terminerar (konsumenten kan bara frigöra plats).
			select {
			case <-h.ch:
				h.dropped.Add(1)
				r.mailboxDropped++
			default:
			}
		}
	}
	r.addOrphanLocked(OrphanEvent{Event: sev.ev, ArrivedAt: sev.at, ActiveQueryAtArrival: sev.gen})
}

// flushLegacyBuffered drains events that were buffered in s.events before
// routing was enabled into the orphan mailbox. Körs enbart på pumpgoroutinen
// (enda skrivaren av s.events) via routing-barriären.
func (s *Session) flushLegacyBuffered() {
	r := s.router
	for {
		select {
		case ev := <-s.events:
			r.mu.Lock()
			r.addOrphanLocked(OrphanEvent{Event: ev, ArrivedAt: time.Now(), ActiveQueryAtArrival: 0})
			r.mu.Unlock()
		default:
			return
		}
	}
}

// addOrphanLocked appends to the orphan ring, dropping the oldest entry when
// full. Caller holds r.mu.
func (r *eventRouter) addOrphanLocked(oe OrphanEvent) {
	if r.count == len(r.orphans) {
		r.orphans[r.head] = OrphanEvent{}
		r.head = (r.head + 1) % len(r.orphans)
		r.count--
		r.orphansDropped++
	}
	r.orphans[(r.head+r.count)%len(r.orphans)] = oe
	r.count++
}

// armQuery registers a new active query generation and returns its handle.
// Ett kvarlämnat tidigare handtag detachas (stängs) — dess redan buffrade
// olästa events kan fortfarande dräneras ur den stängda kanalen av
// innehavaren. Anropas bara efter prepareQuery (sekventiella queries).
func (s *Session) armQuery() *QueryHandle {
	gen := s.genCounter.Add(1)
	h := &QueryHandle{gen: gen, s: s, ch: make(chan Event, queryMailboxCap)}
	r := s.router
	r.mu.Lock()
	if r.shutdown {
		// Sessionen dog mellan prepareQuery och arm. Returnera ett redan
		// stängt handtag i stället för ett som aldrig får events och aldrig
		// stängs — konsumentens range/Wait terminerar direkt i stället för
		// att hänga för evigt (samma hang-klass som rotorsak B).
		h.detached = true
		h.closeReason = errSessionEnded
		close(h.ch)
		r.mu.Unlock()
		return h
	}
	if r.active != nil {
		r.detachLocked(r.active, ErrQueryDetached)
	}
	r.active = h
	r.mu.Unlock()
	// Generationen armas FÖRE stdin-skrivningen (queryRouted), så childens
	// svar på den nya frågan alltid stämplas med rätt generation.
	s.activeGen.Store(gen)
	return h
}

func (r *eventRouter) detach(h *QueryHandle, reason error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detachLocked(h, reason)
}

// detachLocked marks the handle detached and closes its channel. Caller
// holds r.mu. Notera: s.activeGen nollställs INTE här — childen jobbar
// möjligen kvar på queryn, och sena events ska behålla sin generation så
// orphan-poster kan korreleras tillbaka till frågan.
func (r *eventRouter) detachLocked(h *QueryHandle, reason error) {
	if h == nil || h.detached {
		return
	}
	h.detached = true
	h.closeReason = reason
	close(h.ch)
	if r.active == h {
		r.active = nil
	}
}

// shutdownRouter closes the active handle when the session ends so handle
// consumers terminate. No-op when routing was never enabled. Anropas från
// readLoops defer efter att exit-eventet levererats.
func (s *Session) shutdownRouter() {
	if !s.routed.Load() {
		return
	}
	r := s.router
	s.stateMu.Lock()
	reason := s.err
	s.stateMu.Unlock()
	if reason == nil {
		reason = errSessionEnded
	}
	r.mu.Lock()
	if r.active != nil {
		r.detachLocked(r.active, reason)
	}
	r.shutdown = true
	r.mu.Unlock()
}

// DrainOrphans returns and clears the orphan mailbox: events that arrived
// outside any active query, stamped with arrival time and the generation (if
// any) that was active. Anropa före varje ny query — sena färdiga svar från
// avbrutna queries dyker upp här och ska levereras separat, inte kastas.
// Returns nil when routing is not enabled or the mailbox is empty.
func (s *Session) DrainOrphans() []OrphanEvent {
	if !s.routed.Load() {
		return nil
	}
	r := s.router
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	out := make([]OrphanEvent, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.head + i) % len(r.orphans)
		out[i] = r.orphans[idx]
		r.orphans[idx] = OrphanEvent{}
	}
	r.head, r.count = 0, 0
	return out
}

// RouterStats returns drop counters for the bounded router mailboxes.
// Zero-valued when routing is not enabled.
func (s *Session) RouterStats() RouterStats {
	if !s.routed.Load() {
		return RouterStats{}
	}
	r := s.router
	r.mu.Lock()
	defer r.mu.Unlock()
	return RouterStats{OrphansDropped: r.orphansDropped, MailboxDropped: r.mailboxDropped}
}

// QueryCtx sends a user message like Query, but returns a QueryHandle whose
// Events() yields ONLY events stamped for this query — ett buffrat äldre
// ResultEvent kan aldrig läcka in som svar på fel fråga. Första anropet
// aktiverar routat läge för hela sessionen (se EnableRouting): därefter
// levereras inga events på Session.Events(); använd handtaget + DrainOrphans
// och blanda inte med Query()/Events()-läsning på samma session.
//
// ctx avbryter bara inlämningen (kontrolleras före skrivning) — begränsa
// väntan på svar med handle.Wait(ctx) eller egen select på handle.Events().
// Vid skrivfel mot stdin markeras sessionen StateFailed (stdin är förbrukad,
// inga events kommer) och handtaget stängs med felet — sessionen ska då
// kastas och ersättas. Session.Wait() fungerar som vanligt per query.
func (s *Session) QueryCtx(ctx context.Context, prompt string) (*QueryHandle, error) {
	return s.queryRouted(ctx, prompt, nil)
}

// QueryCtxWithContent is QueryCtx with multimodal content blocks; the prompt
// is prepended as a text block (jfr QueryWithContent).
func (s *Session) QueryCtxWithContent(ctx context.Context, prompt string, blocks ...ContentBlock) (*QueryHandle, error) {
	return s.queryRouted(ctx, prompt, blocks)
}

func (s *Session) queryRouted(ctx context.Context, prompt string, blocks []ContentBlock) (*QueryHandle, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	s.EnableRouting()
	if err := s.prepareQuery(); err != nil {
		return nil, err
	}
	h := s.armQuery()
	// Thinking-transitionen emittas efter arm så den stämplas med den nya
	// generationen och landar i handtagets mailbox (samma kontrakt som
	// Query: transitionen syns före CLI:ns svar).
	s.emitQueryActivity()

	var content any = prompt
	if len(blocks) > 0 {
		c := make([]ContentBlock, 0, 1+len(blocks))
		c = append(c, TextBlock(prompt))
		c = append(c, blocks...)
		content = c
	}
	if err := s.sendUserMessage(content); err != nil {
		s.failQuery(err)
		s.router.detach(h, err)
		return nil, err
	}
	return h, nil
}
