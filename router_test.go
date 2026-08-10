package claudecli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- Testhjälpare ---

// waitCond pollar cond tills sant eller deadline — för asynkrona flöden
// (events routas på pumpgoroutinen).
func waitCond(t *testing.T, d time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout (%s): %s", d, msg)
}

// orphanSnapshot kopierar orphan-mailboxen utan att tömma den (in-package
// vy för tester; produktion använder DrainOrphans).
func orphanSnapshot(s *Session) []OrphanEvent {
	if s.router == nil {
		return nil
	}
	r := s.router
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]OrphanEvent, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.orphans[(r.head+i)%len(r.orphans)]
	}
	return out
}

// recvResult läser handtagets events tills ResultEvent, med deadline.
func recvResult(t *testing.T, h *QueryHandle, d time.Duration) *ResultEvent {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-h.Events():
			if !ok {
				t.Fatal("handle stängd före ResultEvent")
			}
			if r, isResult := ev.(*ResultEvent); isResult {
				return r
			}
		case <-deadline:
			t.Fatalf("timeout: inget ResultEvent i handle gen %d", h.Gen())
		}
	}
}

// sendAnswer skickar assistant-text + result utan att stänga stdout.
func (s *sessionSim) sendAnswer(text string) {
	s.send(fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, text))
	s.send(`{"type":"result","subtype":"success","session_id":"test-sess","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":5}}`)
}

// --- Korrelation (rotorsak A) ---

// Två sekventiella QueryCtx får varsitt rätt resultat.
func TestQueryCtxCorrelation(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.sendAnswer("svar1")
		sim.readStdin(t)
		sim.sendAnswer("svar2")
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	h1, err := session.QueryCtx(context.Background(), "fråga1")
	if err != nil {
		t.Fatal(err)
	}
	r1 := recvResult(t, h1, 2*time.Second)
	if r1.Text != "svar1" {
		t.Errorf("h1 result = %q, want 'svar1'", r1.Text)
	}

	h2, err := session.QueryCtx(context.Background(), "fråga2")
	if err != nil {
		t.Fatal(err)
	}
	if h2.Gen() <= h1.Gen() {
		t.Errorf("generationer ska vara stigande: h1=%d h2=%d", h1.Gen(), h2.Gen())
	}
	r2 := recvResult(t, h2, 2*time.Second)
	if r2.Text != "svar2" {
		t.Errorf("h2 result = %q, want 'svar2'", r2.Text)
	}
}

// Regressionstest för +1s-läckan: ett OLÄST färdigt resultat från fråga 1
// får ALDRIG dyka upp som svar på fråga 2. Före fixen buffrades result1 i
// Events()-kanalen och flushades som första event i nästa querys eventloop.
func TestQueryCtxBufferedOldResultDoesNotLeak(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.sendAnswer("gammalt svar")
		sim.readStdin(t)
		sim.sendAnswer("nytt svar")
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	h1, err := session.QueryCtx(context.Background(), "fråga1")
	if err != nil {
		t.Fatal(err)
	}
	// Läs INTE h1 — simulerar att konsumenten övergav queryn. Vänta bara på
	// att resultatet anlänt (state → Idle) så nästa query är tillåten.
	waitCond(t, 2*time.Second, "state Idle efter oläst result", func() bool {
		return session.State() == StateIdle
	})

	h2, err := session.QueryCtx(context.Background(), "fråga2")
	if err != nil {
		t.Fatal(err)
	}
	r2 := recvResult(t, h2, 2*time.Second)
	if r2.Text != "nytt svar" {
		t.Errorf("h2 fick %q — det gamla svaret läckte in i fel query", r2.Text)
	}

	// Det gamla svaret är inte förlorat: h1 stängdes vid supersede men dess
	// buffrade events kan fortfarande dräneras (sen leverans, Tomas-beslutet
	// att färdiga-men-avbrutna svar levereras i stället för att kastas).
	var old *ResultEvent
	for ev := range h1.Events() {
		if r, isResult := ev.(*ResultEvent); isResult {
			old = r
		}
	}
	if old == nil || old.Text != "gammalt svar" {
		t.Errorf("h1:s buffrade result saknas eller fel: %+v", old)
	}
}

// --- Orphan-routing ---

// Ett resultat som anländer när ingen query är aktiv hamnar i
// orphan-mailboxen och läcker inte in i nästa query.
func TestOrphanRoutingNoActiveQuery(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		// Ostimulerat svar — ingen query är aktiv.
		sim.sendAnswer("oombett svar")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	session.EnableRouting()

	// Det ostimulerade resultatet ska dyka upp som orphan med generation 0.
	waitCond(t, 2*time.Second, "oombett result i orphan-mailboxen", func() bool {
		for _, oe := range orphanSnapshot(session) {
			if r, isResult := oe.Event.(*ResultEvent); isResult && r.Text == "oombett svar" {
				return oe.ActiveQueryAtArrival == 0
			}
		}
		return false
	})

	// Bot-mönstret: dränera orphans före ny query.
	orphans := session.DrainOrphans()
	found := false
	for _, oe := range orphans {
		if r, isResult := oe.Event.(*ResultEvent); isResult && r.Text == "oombett svar" {
			found = true
		}
	}
	if !found {
		t.Fatal("DrainOrphans returnerade inte det oombedda resultatet")
	}
	if len(session.DrainOrphans()) != 0 {
		t.Error("DrainOrphans ska tömma mailboxen")
	}

	// Nästa query ser INTE det gamla resultatet.
	go func() {
		sim.readStdin(t)
		sim.sendAnswer("riktigt svar")
	}()
	h, err := session.QueryCtx(context.Background(), "riktig fråga")
	if err != nil {
		t.Fatal(err)
	}
	r := recvResult(t, h, 2*time.Second)
	if r.Text != "riktigt svar" {
		t.Errorf("query fick %q — orphan läckte in", r.Text)
	}
}

// Detach mitt i en query: sena events (t.ex. färdigt resultat efter
// interrupt) hamnar i orphans med queryns generation så de kan korreleras
// tillbaka och levereras separat.
func TestQueryHandleDetachLateResult(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	started := make(chan struct{})
	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"påbörjat"}]}}`)
		<-started
		// "Interrupt" har skett (Detach) — childen blir klar sent.
		sim.sendAnswer("sent färdigt svar")
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	h1, err := session.QueryCtx(context.Background(), "fråga")
	if err != nil {
		t.Fatal(err)
	}

	// Vänta in att arbetet börjat synas i handtaget.
	deadline := time.After(2 * time.Second)
waitText:
	for {
		select {
		case ev, ok := <-h1.Events():
			if !ok {
				t.Fatal("handle stängd i förtid")
			}
			if te, isText := ev.(*TextEvent); isText && te.Content == "påbörjat" {
				break waitText
			}
		case <-deadline:
			t.Fatal("timeout: TextEvent nådde inte handtaget")
		}
	}

	h1.Detach()
	close(started)

	// Det sena resultatet ska landa i orphans, korrelerat till h1:s gen.
	// (ResultEvent.Text ackumulerar all assistant-text sedan förra resultatet,
	// så "påbörjat" ingår också.)
	waitCond(t, 2*time.Second, "sent result i orphans med rätt generation", func() bool {
		for _, oe := range orphanSnapshot(session) {
			if r, isResult := oe.Event.(*ResultEvent); isResult && strings.Contains(r.Text, "sent färdigt svar") {
				return oe.ActiveQueryAtArrival == h1.Gen()
			}
		}
		return false
	})

	// h1 är stängd; Wait ger ErrQueryDetached.
	if _, err := h1.Wait(context.Background()); !errors.Is(err, ErrQueryDetached) {
		t.Errorf("Wait efter Detach = %v, want ErrQueryDetached", err)
	}

	// Sessionen är REN efter det sena resultatet — nästa query fungerar.
	waitCond(t, 2*time.Second, "state Idle efter sent result", func() bool {
		return session.State() == StateIdle
	})
	go func() {
		sim.readStdin(t)
		sim.sendAnswer("svar2")
	}()
	h2, err := session.QueryCtx(context.Background(), "fråga2")
	if err != nil {
		t.Fatal(err)
	}
	if r := recvResult(t, h2, 2*time.Second); r.Text != "svar2" {
		t.Errorf("h2 result = %q, want 'svar2'", r.Text)
	}
}

// Orphan-mailboxen är bounded med drop-oldest + räknare: en tyst konsument
// kan aldrig få routern att blockera readLoop.
func TestOrphanMailboxDropOldest(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	const n = 160 // väl över orphanMailboxCap (128)
	go func() {
		sim.handleInitAndReady(t)
		for i := 0; i < n; i++ {
			sim.send(fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"t%d"}]}}`, i))
		}
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	session.EnableRouting()

	// Vänta tills sista texten routats (ingen konsument läser — routern får
	// aldrig blockera, annars når den här aldrig fram).
	lastText := fmt.Sprintf("t%d", n-1)
	waitCond(t, 5*time.Second, "sista eventet routat till orphans", func() bool {
		snap := orphanSnapshot(session)
		for i := len(snap) - 1; i >= 0; i-- {
			if te, isText := snap[i].Event.(*TextEvent); isText && te.Content == lastText {
				return true
			}
		}
		return false
	})

	snap := orphanSnapshot(session)
	if len(snap) != orphanMailboxCap {
		t.Errorf("orphan-mailbox = %d poster, want cap %d", len(snap), orphanMailboxCap)
	}
	for _, oe := range snap {
		if te, isText := oe.Event.(*TextEvent); isText && te.Content == "t0" {
			t.Error("äldsta eventet t0 skulle ha kastats (drop-oldest)")
		}
	}
	if stats := session.RouterStats(); stats.OrphansDropped == 0 {
		t.Error("OrphansDropped ska räkna kastade events")
	}
}

// Events som buffrats i Events()-kanalen INNAN routing slås på flushas till
// orphan-mailboxen (barriären) i stället för att ligga kvar ospårade.
func TestEnableRoutingFlushesBuffered(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	sent := make(chan struct{})
	go func() {
		sim.handleInit(t)
		sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"buffrad1"}]}}`)
		sim.send(`{"type":"assistant","message":{"content":[{"type":"text","text":"buffrad2"}]}}`)
		close(sent)
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	<-sent
	// Ingen läser Events() — eventen ligger i kanalbufferten (legacy-läget).
	session.EnableRouting()

	waitCond(t, 2*time.Second, "buffrade events flushade till orphans", func() bool {
		var got1, got2 bool
		for _, oe := range orphanSnapshot(session) {
			if te, isText := oe.Event.(*TextEvent); isText {
				if te.Content == "buffrad1" {
					got1 = true
				}
				if te.Content == "buffrad2" {
					got2 = true
				}
			}
		}
		return got1 && got2
	})
}

// --- Hälsa: LastEventAt ---

func TestLastEventAt(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInit(t)
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	// Initialize-handskakningen är control-protokoll, inte events — stämpeln
	// ska vara noll tills första riktiga eventet.
	if !session.LastEventAt().IsZero() {
		t.Error("LastEventAt ska vara noll före första eventet")
	}

	sim.send(`{"type":"system","session_id":"test-sess","model":"sonnet"}`)
	waitCond(t, 2*time.Second, "LastEventAt satt efter första eventet", func() bool {
		return !session.LastEventAt().IsZero()
	})
	first := session.LastEventAt()

	// Även syntetiska events (emitQueryActivity-transitionen vid Query)
	// flyttar stämpeln.
	go func() { sim.readStdin(t) }()
	if err := session.Query("hej"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, 2*time.Second, "LastEventAt flyttad av syntetiskt event", func() bool {
		return session.LastEventAt().After(first) || session.LastEventAt().Equal(first)
	})

	if got := session.ProcessInfo().LastEventAt; got.IsZero() {
		t.Error("ProcessInfo.LastEventAt ska spegla LastEventAt")
	}
}

// --- readLoop-guarden mot syntetiska api-error-messages (rotorsak i
// narration/transport-läckan; samma skydd som ParseEvents parse.go) ---

func TestSessionReadLoopApiErrorGuard(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		// Samma syntetiska fixtur som parse_test.go: claude-cli:s
		// api-error-message när Anthropic-strömmen dör mitt i turen.
		sim.send(`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: The socket connection was closed unexpectedly."}]},"isApiErrorMessage":true}`)
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	if err := session.Query("hej"); err != nil {
		t.Fatal(err)
	}

	// Transportfelet får ALDRIG levereras som svarstext — det ska komma som
	// fatal ErrorEvent.
	deadline := time.After(2 * time.Second)
	var fatalErr error
guardLoop:
	for {
		select {
		case ev, ok := <-session.Events():
			if !ok {
				break guardLoop
			}
			switch e := ev.(type) {
			case *TextEvent:
				if strings.Contains(e.Content, "API Error") {
					t.Fatalf("api-error levererades som TextEvent: %q", e.Content)
				}
			case *ErrorEvent:
				if e.Fatal {
					fatalErr = e.Err
					break guardLoop
				}
			}
		case <-deadline:
			t.Fatal("timeout: ingen fatal ErrorEvent för api-error-message")
		}
	}
	if !errors.Is(fatalErr, ErrAPI) {
		t.Errorf("fatal error = %v, want wrapped ErrAPI", fatalErr)
	}

	// Sessionen är fälld: Wait ger felet och nästa query avvisas.
	if _, err := session.Wait(); !errors.Is(err, ErrAPI) {
		t.Errorf("Wait = %v, want ErrAPI", err)
	}
	if session.State() != StateFailed {
		t.Errorf("state = %v, want StateFailed", session.State())
	}
	if err := session.Query("igen"); err == nil {
		t.Error("Query efter fatal api-error ska avvisas")
	}
}

// I routat läge levereras api-error-guardens fatal ErrorEvent till den
// aktiva queryns handtag så konsumenten ser varför frågan dog.
func TestSessionReadLoopApiErrorGuardRouted(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.send(`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: overloaded"}]},"isApiErrorMessage":true}`)
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	h, err := session.QueryCtx(context.Background(), "hej")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.Wait(ctx); !errors.Is(err, ErrAPI) {
		t.Errorf("handle.Wait = %v, want ErrAPI", err)
	}
}

// --- Handle.Wait ---

func TestQueryHandleWait(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		sim.sendAnswer("svaret")
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	h, err := session.QueryCtx(context.Background(), "fråga")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := h.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "svaret" {
		t.Errorf("Wait result = %q, want 'svaret'", r.Text)
	}
}

func TestQueryHandleWaitCtxCancel(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		// Svara aldrig.
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	h, err := session.QueryCtx(context.Background(), "fråga")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait = %v, want DeadlineExceeded", err)
	}
}

// --- writeStdin-deadline + Close()-deadlocken (rotorsak B) ---

// En hängd child som slutat läsa stdin får inte blockera writeStdin för
// evigt: skrivningen ska ge fel inom deadlinen och sessionen fällas.
func TestWriteStdinDeadline(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi, WithStdinWriteTimeout(150*time.Millisecond))

	go func() {
		sim.handleInit(t)
		// Läs ALDRIG mer från stdin — simulerar hängd child med full pipe.
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	start := time.Now()
	_, err = session.QueryCtx(context.Background(), "hänger")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("QueryCtx mot igenproppad stdin ska ge fel")
	}
	if !strings.Contains(err.Error(), "stdin write timeout") {
		t.Errorf("fel = %v, want stdin write timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("skrivningen tog %s — deadlinen (150ms) verkar inte gälla", elapsed)
	}

	// Stdin är förbrukad: sessionen är fälld och nästa query avvisas direkt.
	if session.State() != StateFailed {
		t.Errorf("state = %v, want StateFailed efter skriv-timeout", session.State())
	}
	if _, err := session.QueryCtx(context.Background(), "igen"); err == nil {
		t.Error("QueryCtx efter fälld session ska avvisas")
	}
}

// Ping är användbar som watchdog även mot en igenproppad stdin-pipe:
// bounded av sin timeout i stället för att hänga i writeStdin.
func TestPingBoundedOnWedgedStdin(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInit(t)
		// Läs aldrig ping-requesten.
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sim.bidi.StdoutWriter.Close()
		session.Close()
	}()

	start := time.Now()
	err = session.Ping(100 * time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Ping mot igenproppad stdin ska ge fel")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("fel = %v, want timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Ping tog %s — inte bounded", elapsed)
	}
}

// Close() får aldrig deadlocka mot en blockerad stdin-skrivning. Före fixen
// höll writeStdin s.mu under en oändligt blockerad Write, och Close() —
// som också tar s.mu — hängde för evigt (kill-vägen var död).
func TestSessionCloseNoDeadlockOnBlockedWrite(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi, WithStdinWriteTimeout(200*time.Millisecond))

	go func() {
		sim.handleInit(t)
		// Läs aldrig — Query-skrivningen blockerar i io.Pipe.
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	qErrCh := make(chan error, 1)
	go func() { qErrCh <- session.Query("blockerad") }()
	// Ge skrivningen tid att blockera med s.mu hållen.
	time.Sleep(50 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close() }()
	// Släpp readLoop så Close kan slutföra när mu väl frigjorts.
	time.Sleep(50 * time.Millisecond)
	sim.bidi.StdoutWriter.Close()

	select {
	case <-closeDone:
		// OK — Close kom igenom inom skriv-deadlinen + städning.
	case <-time.After(5 * time.Second):
		t.Fatal("Close() deadlockade mot blockerad stdin-skrivning")
	}

	select {
	case qErr := <-qErrCh:
		if qErr == nil {
			t.Error("blockerad Query ska ge fel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Query returnerade aldrig")
	}
}

// armQuery efter router-shutdown (sessionen dog mellan prepareQuery och arm)
// ska ge ett redan stängt handtag — aldrig ett som hänger för evigt.
func TestArmQueryAfterShutdownReturnsClosedHandle(t *testing.T) {
	s := &Session{router: newEventRouter()}
	s.routed.Store(true)
	s.shutdownRouter()

	h := s.armQuery()
	select {
	case _, ok := <-h.Events():
		if ok {
			t.Fatal("stängt handtag ska inte leverera events")
		}
	default:
		t.Fatal("handtag från nedstängd router ska vara stängt direkt")
	}
	if _, err := h.Wait(context.Background()); !errors.Is(err, errSessionEnded) {
		t.Errorf("Wait = %v, want errSessionEnded", err)
	}
}

// Sessionsslut stänger aktivt handtag med orsaken så konsumenter terminerar.
func TestRouterShutdownClosesActiveHandle(t *testing.T) {
	sim := newSessionSim()
	client := NewWithExecutor(sim.bidi)

	go func() {
		sim.handleInitAndReady(t)
		sim.readStdin(t)
		// Stäng stdout mitt i queryn — sessionen dör utan result.
		sim.bidi.StdoutWriter.Close()
	}()

	session, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	h, err := session.QueryCtx(context.Background(), "fråga")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.Wait(ctx); err == nil {
		t.Error("Wait ska ge fel när sessionen dör utan result")
	}
}
