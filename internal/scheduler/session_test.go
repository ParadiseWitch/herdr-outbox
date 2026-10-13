package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/store"
)

// multiSession is a backend over three herdr servers, two of which counted out the
// *same* ids. Fake models a single session, so the merge MultiBackend does in
// production has to be reproduced by hand: without it, nothing in the scheduler
// ever sees a pane belonging to a session other than the primary.
//
//	default  running, has an agent
//	note     running, hosts no agents, so its state_change_seq read comes back empty
//	jx       stopped: its panes are in history but not in any snapshot
type multiSession struct {
	t     *testing.T
	live  *herdr.Fake
	quiet *herdr.Fake
	sent  []herdr.FakeSend
	// primaryErr, when set, is why the primary session could not be read this
	// round.
	primaryErr string
}

func newMultiSession(t *testing.T) *multiSession {
	t.Helper()
	live := herdr.NewFake()
	live.Session = "default"
	live.Workspaces = []herdr.Workspace{{ID: "w1", Label: "dev"}}
	live.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Agent: "qodercli", AgentStatus: herdr.StatusWorking})

	quiet := herdr.NewFake()
	quiet.Session = "note"
	quiet.Workspaces = []herdr.Workspace{{ID: "w1", Label: "笔记"}}
	quiet.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", AgentStatus: herdr.StatusUnknown})

	return &multiSession{t: t, live: live, quiet: quiet}
}

func (m *multiSession) Name() string                { return "herdr-multi-test" }
func (m *multiSession) Probe(context.Context) error { return nil }

func (m *multiSession) Snapshot(ctx context.Context) (*herdr.Snapshot, error) {
	a, err := m.live.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	b, err := m.quiet.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &herdr.Snapshot{
		Primary:    "default",
		Workspaces: append(append([]herdr.Workspace{}, a.Workspaces...), b.Workspaces...),
		Tabs:       append(append([]herdr.Tab{}, a.Tabs...), b.Tabs...),
		Panes:      append(append([]herdr.Pane{}, a.Panes...), b.Panes...),
		Sessions: []herdr.Session{
			{Name: "default", Running: true, Default: true, Err: m.primaryErr},
			{Name: "note", Running: true},
			{Name: "jx", Running: false},
		},
		// A session hosting no agents reports no roster, so there is no sequence
		// from which to prove a completion. That is about `note` alone.
		SeqUnavailable: map[string]string{"note": "fake: no agent roster"},
	}, nil
}

// Send records the id it was handed. Routing is the point: a backend serving
// several sessions can only pick the right terminal if the id names one.
func (m *multiSession) Send(ctx context.Context, paneID, text string) (herdr.SendResult, error) {
	m.sent = append(m.sent, herdr.FakeSend{PaneID: paneID, Text: text})
	session, bare := herdr.SplitQualified(paneID)
	switch session {
	case "default":
		return m.live.Send(ctx, bare, text)
	case "note":
		return m.quiet.Send(ctx, bare, text)
	}
	return herdr.SendResult{}, &herdr.CLIError{Code: "server_not_running", Message: "session " + session + " is not running"}
}

func (m *multiSession) Focus(ctx context.Context, paneID string) error {
	_, bare := herdr.SplitQualified(paneID)
	return m.live.Focus(ctx, bare)
}

func (m *multiSession) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	_, bare := herdr.SplitQualified(paneID)
	return m.live.ReadPane(ctx, bare, lines)
}

type multiFixture struct {
	t      *testing.T
	engine *Engine
	be     *multiSession
	st     *store.Store
	now    time.Time
}

func multiFixtureFor(t *testing.T) *multiFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "outbox")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	led, err := OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	be := newMultiSession(t)
	eng := NewEngine(st, be, led)
	eng.SnapshotMaxAge = 0
	f := &multiFixture{t: t, engine: eng, be: be, st: st, now: time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)}
	eng.Now = func() time.Time { return f.now }
	return f
}

func (f *multiFixture) step(d time.Duration) []Event {
	f.now = f.now.Add(d)
	evs, err := f.engine.Step(context.Background())
	if err != nil {
		f.t.Fatalf("step: %v", err)
	}
	return evs
}

func (f *multiFixture) add(content string, target model.Target, kind model.TriggerKind) *model.Message {
	f.t.Helper()
	m, err := f.st.Create(&model.Message{
		Content: content, Target: target,
		Trigger: model.Trigger{Kind: kind, SettleSeconds: 3},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// arm records the baseline the way arming from the TUI does.
func (f *multiFixture) arm(m *model.Message) *model.Message {
	f.t.Helper()
	if _, err := f.engine.Arm(context.Background(), m); err != nil && !herdr.IsUnavailable(err) {
		f.t.Fatalf("arm: %v", err)
	}
	if err := f.st.Save(m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *multiFixture) get(id string) *model.Message {
	f.t.Helper()
	m, err := f.st.Get(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// `w1:p1` exists in two sessions. The send has to name the owner, or the text
// lands in the primary's terminal while the message claims it went elsewhere.
func TestSendRoutesToTheOwningSession(t *testing.T) {
	f := multiFixtureFor(t)
	foreign := f.add("发给 note\n", model.Target{Session: "note", Workspace: "w1", Pane: "w1:p1"}, model.TriggerManual)
	if _, err := f.engine.SendNow(context.Background(), foreign.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(f.be.sent) != 1 || f.be.sent[0].PaneID != "note/w1:p1" {
		t.Fatalf("routed to %v, want note/w1:p1", f.be.sent)
	}
}

// An unqualified target is the primary session's, which is what every message
// written before multi-session support meant.
func TestUnqualifiedTargetStaysInPrimarySession(t *testing.T) {
	f := multiFixtureFor(t)
	own := f.add("发给本机\n", model.Target{Workspace: "w1", Pane: "w1:p1"}, model.TriggerManual)
	if _, err := f.engine.SendNow(context.Background(), own.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(f.be.sent) != 1 || f.be.sent[0].PaneID != "default/w1:p1" {
		t.Fatalf("routed to %v, want default/w1:p1", f.be.sent)
	}
}

// A stopped session's pane is not a disappeared pane. Saying so is the difference
// between "start that session and it will go" and a message the user will rewrite.
func TestStoppedSessionIsNotReportedAsMissingTarget(t *testing.T) {
	f := multiFixtureFor(t)
	m := f.add("等 jx 启动\n", model.Target{Session: "jx", Workspace: "w1", Pane: "w1:p1"}, model.TriggerManual)
	evs, err := f.engine.SendNow(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(f.be.sent) != 0 {
		t.Fatalf("a stopped session received %v", f.be.sent)
	}
	got := f.get(m.ID)
	if got.Status == model.StatusSent {
		t.Fatal("a stopped session reported a delivery")
	}
	if got.Status == model.StatusFailed {
		t.Fatalf("a stopped session is not the message's fault: %q", got.LastError)
	}
	if countKind(evs, EventDeferred) != 1 {
		t.Fatalf("the hold said nothing: %v", evs)
	}

	// The rest of herdr keeps working while that message waits.
	own := f.add("本机可发\n", model.Target{Session: "default", Workspace: "w1", Pane: "w1:p1"}, model.TriggerManual)
	if _, err := f.engine.SendNow(context.Background(), own.ID); err != nil {
		t.Fatalf("send while another session is stopped: %v", err)
	}
	if len(f.be.sent) != 1 || f.be.sent[0].PaneID != "default/w1:p1" {
		t.Fatalf("the running session was blocked by the stopped one: %v", f.be.sent)
	}
}

// A scheduled message aimed at a stopped session holds at its fire time rather
// than burning the send attempt on a socket that is not there.
func TestScheduledMessageHoldsForStoppedSession(t *testing.T) {
	f := multiFixtureFor(t)
	sendAt := f.now.Add(30 * time.Second)
	m := f.add("到点发给 jx\n", model.Target{Session: "jx", Workspace: "w1", Pane: "w1:p1"}, model.TriggerScheduled)
	m.Trigger.SendAt = &sendAt
	m = f.arm(m)

	f.step(60 * time.Second)
	if len(f.be.sent) != 0 {
		t.Fatalf("fired into a stopped session: %v", f.be.sent)
	}
	if got := f.get(m.ID); got.Status == model.StatusFailed {
		t.Fatalf("the session being down failed the message: %q", got.LastError)
	}
}

// A session herdr could not read this round is transient: the message keeps its
// place and stays quiet. One event per tick would bury the log, and a failure
// verdict would be a lie about the message.
func TestUnreadableSessionHoldsQuietlyThenRecovers(t *testing.T) {
	f := multiFixtureFor(t)
	m := f.arm(f.add("先别发\n", model.Target{Session: "default", Workspace: "w1", Pane: "w1:p1"}, model.TriggerAfterCompletion))
	f.be.primaryErr = "connection refused"

	var seen []Event
	for i := 0; i < 5; i++ {
		seen = append(seen, f.step(time.Second)...)
	}
	if len(f.be.sent) != 0 {
		t.Fatalf("sent into an unreadable session: %v", f.be.sent)
	}
	if n := countKind(seen, EventFailed); n != 0 {
		t.Fatalf("a read failure became %d delivery failures", n)
	}
	if n := countKind(seen, EventDeferred); n != 0 {
		t.Fatalf("an unreadable session announced itself %d times, want silence", n)
	}
	if got := f.get(m.ID); got.Status != model.StatusWaiting {
		t.Fatalf("status = %v, want the message still waiting", got.Status)
	}

	// Once herdr answers again, the completion edge has to be catchable.
	f.be.primaryErr = ""
	f.be.live.SetStatus("w1:p1", herdr.StatusIdle)
	for i := 0; i < 12 && len(f.be.sent) == 0; i++ {
		f.step(time.Second)
	}
	if len(f.be.sent) != 1 || f.be.sent[0].PaneID != "default/w1:p1" {
		t.Fatalf("the message never recovered: %v", f.be.sent)
	}
}

// state_change_seq comes from the agent read, and a session hosting no agents
// legitimately reports none. That verdict is per session: it must not disarm a
// completion aimed at a session that does have an agent.
func TestMissingAgentSeqIsPerSession(t *testing.T) {
	f := multiFixtureFor(t)
	primary := f.arm(f.add("本机完成后发\n",
		model.Target{Session: "default", Workspace: "w1", Pane: "w1:p1"}, model.TriggerAfterCompletion))

	// Arming a completion on the agentless session has to say why, instead of
	// parking a message that can never observe a transition.
	quiet := f.add("note 无代理\n", model.Target{Session: "note", Workspace: "w1", Pane: "w1:p1"}, model.TriggerAfterCompletion)
	if _, err := f.engine.Arm(context.Background(), quiet); err == nil {
		t.Fatal("arming on a session with no agent seq claimed success")
	}

	f.be.live.SetStatus("w1:p1", herdr.StatusWorking)
	f.step(time.Second)
	f.be.live.SetStatus("w1:p1", herdr.StatusIdle)
	for i := 0; i < 12 && len(f.be.sent) == 0; i++ {
		f.step(time.Second)
	}
	if len(f.be.sent) != 1 || f.be.sent[0].PaneID != "default/w1:p1" {
		t.Fatalf("the primary was disarmed by another session's missing agent: %v", f.be.sent)
	}
	if got := f.get(primary.ID); got.Status != model.StatusSent {
		t.Fatalf("primary status = %v, want sent", got.Status)
	}
}
