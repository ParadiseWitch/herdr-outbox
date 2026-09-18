package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/store"
)

const settle = 3 * time.Second

type fixture struct {
	t      *testing.T
	engine *Engine
	fake   *herdr.Fake
	st     *store.Store
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
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
	fake := herdr.NewFake()
	fake.Workspaces = []herdr.Workspace{{ID: "w1", Label: "dev"}}
	fake.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", Agent: "qodercli", AgentStatus: herdr.StatusWorking})
	fake.AddPane(herdr.Pane{ID: "w1:p2", WorkspaceID: "w1"})
	eng := NewEngine(st, fake, led)
	// Tests drive their own ticks, so they always read herdr state as of the tick.
	eng.SnapshotMaxAge = 0
	f := &fixture{t: t, engine: eng, fake: fake, st: st, now: time.Date(2026, 9, 16, 10, 0, 0, 0, time.Local)}
	eng.Now = func() time.Time { return f.now }
	return f
}

// tick advances the clock and runs one scheduler step.
func (f *fixture) tick(d time.Duration) []Event {
	f.t.Helper()
	f.now = f.now.Add(d)
	evs, err := f.engine.Step(context.Background())
	if err != nil {
		f.t.Fatalf("step: %v", err)
	}
	return evs
}

func (f *fixture) newMsg(body string, target model.Target, kind model.TriggerKind) *model.Message {
	f.t.Helper()
	m, err := f.st.Create(&model.Message{Content: body, Target: target,
		Trigger: model.Trigger{Kind: kind, SettleSeconds: int(settle / time.Second)}})
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) arm(m *model.Message) *model.Message {
	f.t.Helper()
	if _, err := f.engine.Arm(context.Background(), m); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.Save(m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

// driveCompletion moves the pane working -> idle, then keeps ticking well past
// the settle window the way the daemon would.
func (f *fixture) driveCompletion(paneID string) []Event {
	f.t.Helper()
	f.fake.SetStatus(paneID, herdr.StatusWorking)
	var out []Event
	out = append(out, f.tick(time.Second)...)
	f.fake.SetStatus(paneID, herdr.StatusIdle)
	for i := 0; i < 8; i++ {
		out = append(out, f.tick(time.Second)...)
	}
	return out
}

func (f *fixture) reload(id string) *model.Message {
	f.t.Helper()
	m, err := f.st.Get(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func countKind(evs []Event, k EventKind) int {
	n := 0
	for _, e := range evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

var target = model.Target{Workspace: "w1", Pane: "w1:p1"}

func TestAfterCompletionFiresOnWorkingToIdle(t *testing.T) {
	f := newFixture(t)
	m := f.arm(f.newMsg("run the tests\n", target, model.TriggerAfterCompletion))
	if !m.ObservedWorking || m.BaselineSeq == 0 {
		t.Fatalf("expected armed baseline, got %+v", m)
	}
	if evs := f.tick(time.Second); len(evs) != 0 {
		t.Fatalf("should wait while working, got %v", evs)
	}

	f.fake.SetStatus("w1:p1", herdr.StatusWorking)
	f.tick(time.Second)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	// The idle state has to survive the settle window before anything is sent.
	for i := 0; i < 3; i++ {
		if evs := f.tick(time.Second); countKind(evs, EventSent) != 0 {
			t.Fatalf("sent at %ds into a %s settle window: %v", i, settle, evs)
		}
	}
	evs := f.tick(time.Second)
	if countKind(evs, EventSent) != 1 {
		t.Fatalf("expected one send, got %v", evs)
	}
	sends := f.fake.Sends()
	if len(sends) != 1 || sends[0].Surface != "agent" {
		t.Fatalf("want one agent send, got %+v", sends)
	}
	if sends[0].Text != "run the tests\n" {
		t.Fatalf("sent wrong text: %q", sends[0].Text)
	}
	got := f.reload(m.ID)
	if got.Status != model.StatusSent || got.SentAt == nil || got.SentVia != "agent" {
		t.Fatalf("want sent, got %+v", got)
	}

	// A later completion of the same message must not re-fire it.
	if evs := f.driveCompletion("w1:p1"); countKind(evs, EventSent) != 0 {
		t.Fatalf("sent message re-fired: %v", evs)
	}
	if len(f.fake.Sends()) != 1 {
		t.Fatalf("re-fired: %+v", f.fake.Sends())
	}
}

func TestAfterCompletionArmedWhileIdleWaitsForNewWork(t *testing.T) {
	f := newFixture(t)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	m := f.arm(f.newMsg("next task\n", target, model.TriggerAfterCompletion))
	if m.ObservedWorking {
		t.Fatal("armed on an idle pane should not claim a working agent")
	}
	for i := 0; i < 4; i++ {
		if evs := f.tick(time.Second); len(evs) != 0 {
			t.Fatalf("idle pane with no transition produced events: %v", evs)
		}
	}
	if len(f.fake.Sends()) != 0 {
		t.Fatalf("sent without a completion edge: %+v", f.fake.Sends())
	}
	if evs := f.driveCompletion("w1:p1"); countKind(evs, EventSent) != 1 {
		t.Fatalf("expected send after the next completion, got %v", evs)
	}
	if got := f.reload(m.ID); got.Status != model.StatusSent {
		t.Fatalf("want sent, got %s", got.Status)
	}
}

func TestAfterCompletionIgnoresJitter(t *testing.T) {
	f := newFixture(t)
	f.arm(f.newMsg("jitter\n", target, model.TriggerAfterCompletion))
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	f.tick(time.Second)
	// Idle for a moment, then straight back to working: the window restarts.
	for i := 0; i < 2; i++ {
		if evs := f.tick(time.Second); countKind(evs, EventSent) != 0 {
			t.Fatalf("premature send: %v", evs)
		}
	}
	f.fake.SetStatus("w1:p1", herdr.StatusWorking)
	f.tick(time.Second)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	for i := 0; i < 3; i++ {
		if evs := f.tick(time.Second); countKind(evs, EventSent) != 0 {
			t.Fatalf("send during a flicker: %v", evs)
		}
	}
	f.tick(time.Second)
	if len(f.fake.Sends()) != 1 {
		t.Fatalf("want exactly one send, got %+v", f.fake.Sends())
	}
}

func TestAfterCompletionHoldsWhileBlocked(t *testing.T) {
	f := newFixture(t)
	f.arm(f.newMsg("blocked\n", target, model.TriggerAfterCompletion))
	f.fake.SetStatus("w1:p1", herdr.StatusBlocked)
	for i := 0; i < 6; i++ {
		if evs := f.tick(time.Second); countKind(evs, EventSent) != 0 {
			t.Fatalf("sent into an approval dialog: %v", evs)
		}
	}
	f.fake.SetStatus("w1:p1", herdr.StatusWorking)
	f.tick(time.Second)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	var evs []Event
	for i := 0; i < 6 && countKind(evs, EventSent) == 0; i++ {
		evs = append(evs, f.tick(time.Second)...)
	}
	if countKind(evs, EventSent) != 1 {
		t.Fatalf("expected send after unblocking, got %v", evs)
	}
}

func TestAfterCompletionSurvivesMissedPoll(t *testing.T) {
	f := newFixture(t)
	f.arm(f.newMsg("missed poll\n", target, model.TriggerAfterCompletion))
	// The working -> idle cycle happens entirely between two polls.
	f.fake.SetStatus("w1:p1", herdr.StatusWorking)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	var evs []Event
	for i := 0; i < 6; i++ {
		evs = append(evs, f.tick(time.Second)...)
	}
	if countKind(evs, EventSent) != 1 {
		t.Fatalf("state_change_seq edge should survive a missed poll, got %v", evs)
	}
}

func TestScheduledFiresAtTime(t *testing.T) {
	f := newFixture(t)
	sendAt := f.now.Add(30 * time.Second)
	m := f.newMsg("nightly build\n", target, model.TriggerScheduled)
	m.Trigger.SendAt = &sendAt
	m = f.arm(m)
	if m.Status != model.StatusScheduled {
		t.Fatalf("want scheduled, got %s", m.Status)
	}
	if evs := f.tick(20 * time.Second); countKind(evs, EventSent) != 0 {
		t.Fatalf("fired early: %v", evs)
	}
	if evs := f.tick(15 * time.Second); countKind(evs, EventSent) != 1 {
		t.Fatalf("expected fire, got %v", evs)
	}
	if f.reload(m.ID).Status != model.StatusSent {
		t.Fatal("want sent")
	}
}

func TestManualSendUsesPaneSurfaceForShells(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("echo hi\n", model.Target{Workspace: "w1", Pane: "w1:p2"}, model.TriggerManual)
	if m.Status != model.StatusDraft {
		t.Fatalf("new messages start as draft, got %s", m.Status)
	}
	evs, err := f.engine.SendNow(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if countKind(evs, EventSent) != 1 {
		t.Fatalf("want sent, got %v", evs)
	}
	sends := f.fake.Sends()
	if len(sends) != 1 || sends[0].Surface != "pane" {
		t.Fatalf("want one pane send, got %+v", sends)
	}
}

func TestManualSendTwiceIsAllowed(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("again\n", target, model.TriggerManual)
	ctx := context.Background()
	if _, err := f.engine.SendNow(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := f.engine.SendNow(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.Sends()) != 2 {
		t.Fatalf("explicit retry blocked: %+v", f.fake.Sends())
	}
}

func TestMissingTargetFails(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("ghost\n", model.Target{Workspace: "w1", Pane: "w9:p9"}, model.TriggerManual)
	if _, err := f.engine.SendNow(context.Background(), m.ID); err != nil {
		t.Log(err)
	}
	got := f.reload(m.ID)
	if got.Status != model.StatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if got.LastError == "" {
		t.Fatal("want an error explanation")
	}
}

func TestWaitingTargetGoneDefersWithoutFailing(t *testing.T) {
	f := newFixture(t)
	m := f.arm(f.newMsg("gone\n", model.Target{Workspace: "w1", Pane: "w1:p1"}, model.TriggerAfterCompletion))
	f.fake.RemovePane("w1:p1")
	evs := f.tick(time.Second)
	if countKind(evs, EventDeferred) != 1 {
		t.Fatalf("want a deferred notice, got %v", evs)
	}
	got := f.reload(m.ID)
	if got.Status != model.StatusWaiting {
		t.Fatalf("a vanished pane must not fail the message, got %s", got.Status)
	}
}

func TestBlockedSendHoldsForNextCompletion(t *testing.T) {
	f := newFixture(t)
	m := f.arm(f.newMsg("hold\n", target, model.TriggerAfterCompletion))
	f.fake.Blocked["w1:p1"] = true
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	var evs []Event
	for i := 0; i < 8; i++ {
		evs = append(evs, f.tick(time.Second)...)
	}
	if countKind(evs, EventSent) != 0 {
		t.Fatalf("send landed into an approval dialog: %v", evs)
	}
	if countKind(evs, EventDeferred) == 0 {
		t.Fatalf("expected the send to be reported as held, got %v", evs)
	}
	if len(f.fake.Sends()) != 0 {
		t.Fatalf("backend was called while blocked: %+v", f.fake.Sends())
	}
	if got := f.reload(m.ID); got.Status != model.StatusWaiting {
		t.Fatalf("a blocked agent must leave the message waiting, got %s", got.Status)
	}
}

func TestCrashAfterAckRecoversAsSent(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("acked\n", target, model.TriggerManual)
	if err := f.st.Save(m); err != nil {
		t.Fatal(err)
	}
	version := m.Version()
	if err := f.engine.Ledger.Append(Record{ID: m.ID, Version: version, Phase: PhaseAcked, Pane: "w1:p1", Surface: "agent"}); err != nil {
		t.Fatal(err)
	}
	m.Status = model.StatusSending
	if err := f.st.Save(m); err != nil {
		t.Fatal(err)
	}

	restarted := NewEngine(f.st, f.fake, mustLedger(t, f.engine.Ledger.path))
	restarted.Now = func() time.Time { return f.now.Add(time.Hour) }
	evs := restarted.Reconcile(context.Background())
	if countKind(evs, EventRecovered) != 1 {
		t.Fatalf("want recovery event, got %v", evs)
	}
	got, err := restarted.Store.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusSent {
		t.Fatalf("acked revision must settle as sent, got %s", got.Status)
	}
	if len(f.fake.Sends()) != 0 {
		t.Fatalf("reconcile must never send: %+v", f.fake.Sends())
	}
}

func TestCrashWithUnknownOutcomeMarksFailed(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("crashed\n", target, model.TriggerAfterCompletion)
	m = f.arm(m)
	m.Status = model.StatusSending
	if err := f.st.Save(m); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Ledger.Append(Record{ID: m.ID, Version: m.Version(), Phase: PhaseAttempted, Pane: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	restarted := NewEngine(f.st, f.fake, mustLedger(t, f.engine.Ledger.path))
	restarted.Now = func() time.Time { return f.now.Add(time.Hour) }
	evs := restarted.Reconcile(context.Background())
	if countKind(evs, EventRecovered) != 1 {
		t.Fatalf("want one recovery event, got %v", evs)
	}
	got, _ := restarted.Store.Get(m.ID)
	if got.Status != model.StatusFailed {
		t.Fatalf("unknown outcome must not auto-resend, got %s", got.Status)
	}
	if got.LastError == "" {
		t.Fatal("want an explanation for the user")
	}
	before := len(f.fake.Sends())
	restarted.Step(context.Background())
	if len(f.fake.Sends()) != before {
		t.Fatalf("restart caused a resend: %+v", f.fake.Sends())
	}
}

func TestLedgerVetoesRepeatedAutoSend(t *testing.T) {
	f := newFixture(t)
	m := f.arm(f.newMsg("veto\n", target, model.TriggerAfterCompletion))
	if evs := f.driveCompletion("w1:p1"); countKind(evs, EventSent) != 1 {
		t.Fatalf("want one send, got %v", evs)
	}
	// Hand-edit the file back to waiting without touching updated_at: the same
	// revision must be refused by the ledger rather than sent again.
	stored := f.reload(m.ID)
	stored.Status = model.StatusWaiting
	stored.SentAt = nil
	stored.BaselineSeq = 0
	stored.ObservedWorking = false
	if err := f.st.Save(stored); err != nil {
		t.Fatal(err)
	}
	if evs := f.driveCompletion("w1:p1"); countKind(evs, EventSkipped) == 0 {
		t.Fatalf("ledger did not veto the resend: %v", evs)
	}
	if len(f.fake.Sends()) != 1 {
		t.Fatalf("resend escaped the ledger: %+v", f.fake.Sends())
	}
	if got := f.reload(m.ID); got.Status != model.StatusSent {
		t.Fatalf("vetoed revision should settle as sent, got %s", got.Status)
	}
}

func TestSettleWindowIsConfigurable(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("slow settle\n", target, model.TriggerAfterCompletion)
	m.Trigger.SettleSeconds = 30
	m = f.arm(m)
	f.fake.SetStatus("w1:p1", herdr.StatusWorking)
	f.tick(time.Second)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	for i := 0; i < 10; i++ {
		if evs := f.tick(2 * time.Second); countKind(evs, EventSent) != 0 {
			t.Fatalf("sent before the 30s window elapsed: %v", evs)
		}
	}
	if got := f.reload(m.ID); got.Status != model.StatusWaiting {
		t.Fatalf("still waiting expected, got %s", got.Status)
	}
	var evs []Event
	for i := 0; i < 10; i++ {
		evs = append(evs, f.tick(2*time.Second)...)
		if countKind(evs, EventSent) == 1 {
			break
		}
	}
	if countKind(evs, EventSent) != 1 {
		t.Fatalf("expected a send past the window, got %v", evs)
	}
	if got := f.reload(m.ID); got.Status != model.StatusSent {
		t.Fatalf("want sent, got %s", got.Status)
	}
}

func TestTargetResolutionAcceptsLabelAndID(t *testing.T) {
	f := newFixture(t)
	snap, err := f.engine.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := snap.Resolve(model.Target{Workspace: "dev", Pane: "w1:p1"}); !ok || p.ID != "w1:p1" {
		t.Fatalf("workspace label + pane id failed: %+v %v", p, ok)
	}
	if p, ok := snap.Resolve(model.Target{Workspace: "w1", Pane: "qodercli-1"}); !ok || p.ID != "w1:p1" {
		t.Fatalf("ordinal name failed: %+v %v", p, ok)
	}
	if p, ok := snap.Resolve(model.Target{Workspace: "w1", Pane: "p1"}); !ok || p.ID != "w1:p1" {
		t.Fatalf("bare pane suffix failed: %+v %v", p, ok)
	}
	if _, ok := snap.Resolve(model.Target{Workspace: "w1", Pane: "nope-9"}); ok {
		t.Fatal("bogus target resolved")
	}
}

func TestSendFailureMarksFailed(t *testing.T) {
	f := newFixture(t)
	f.fake.FailSend = &herdr.CLIError{Code: "socket_closed", Message: "herdr server went away"}
	m := f.newMsg("boom\n", target, model.TriggerManual)
	evs, err := f.engine.SendNow(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("send failure is reported as an event: %v", err)
	}
	if countKind(evs, EventFailed) != 1 {
		t.Fatalf("want a failed event, got %v", evs)
	}
	got := f.reload(m.ID)
	if got.Status != model.StatusFailed || got.Attempts != 1 {
		t.Fatalf("want failed with one attempt, got %+v", got)
	}
	if !strings.Contains(got.LastError, "socket_closed") {
		t.Fatalf("error code lost: %q", got.LastError)
	}
}

func mustLedger(t *testing.T, path string) *Ledger {
	t.Helper()
	l, err := OpenLedger(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLedgerSurvivesReopen(t *testing.T) {
	dir, err := os.MkdirTemp("", "ledger")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	l, err := OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{ID: "a", Version: "v1", Phase: PhaseAttempted}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{ID: "a", Version: "v1", Phase: PhaseAcked, Surface: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{ID: "b", Version: "v1", Phase: PhaseAttempted}); err != nil {
		t.Fatal(err)
	}
	reopened := mustLedger(t, l.path)
	if _, ok := reopened.Acked("a", "v1"); !ok {
		t.Fatal("lost ack after reopen")
	}
	if _, ok := reopened.Open("b", "v1"); !ok {
		t.Fatal("lost open attempt after reopen")
	}
	if _, ok := reopened.Acked("b", "v1"); ok {
		t.Fatal("unacked attempt reported as acked")
	}
}

// shellPane models what `herdr pane list` reports for a plain terminal: a status
// of `unknown` and no row in `agent list`, so no state_change_seq either.
func TestAfterCompletionRejectsPaneWithoutAgent(t *testing.T) {
	f := newFixture(t)
	f.fake.SetStatus("w1:p2", herdr.StatusUnknown)
	m := f.newMsg("to a shell\n", model.Target{Workspace: "w1", Pane: "w1:p2"}, model.TriggerAfterCompletion)
	if _, err := f.engine.Arm(context.Background(), m); err == nil {
		t.Fatal("arming on an unclassifiable pane must be refused, not parked silently")
	}
}

// A message that was armed while the pane had an agent, and lost it afterwards,
// has to explain itself once instead of sitting in `waiting` with no clue.
func TestAfterCompletionExplainsAgentDisappearing(t *testing.T) {
	f := newFixture(t)
	m := f.arm(f.newMsg("orphaned\n", target, model.TriggerAfterCompletion))
	f.fake.SetStatus("w1:p1", herdr.StatusUnknown)
	var seen []Event
	for i := 0; i < 10; i++ {
		seen = append(seen, f.tick(time.Second)...)
	}
	if countKind(seen, EventMissed) != 1 {
		t.Fatalf("want one explanation across 10 ticks, got %v", seen)
	}
	if got := f.reload(m.ID); got.LastError == "" {
		t.Fatal("the stall has to be recorded on the message")
	}
}

// `agent list` is the only read carrying state_change_seq. When it fails, every
// pane reads seq 0 while still looking usable, which used to re-bind the baseline
// forever and never fire.
func TestAfterCompletionRefusesWhenSequenceReadFails(t *testing.T) {
	f := newFixture(t)
	m := f.arm(f.newMsg("no seq\n", target, model.TriggerAfterCompletion))
	f.fake.NoAgentSeq = true
	var seen []Event
	for i := 0; i < 10; i++ {
		seen = append(seen, f.tick(time.Second)...)
	}
	if countKind(seen, EventSent) != 0 {
		t.Fatalf("sent without a completion edge: %+v", f.fake.Sends())
	}
	if countKind(seen, EventDeferred) != 1 {
		t.Fatalf("want exactly one warning, got %v", seen)
	}
	if got := f.reload(m.ID); got.Status != model.StatusWaiting {
		t.Fatalf("want the message held, got %v", got.Status)
	}

	f.fake.NoAgentSeq = false
	f.fake.SetStatus("w1:p1", herdr.StatusWorking)
	f.tick(time.Second)
	f.fake.SetStatus("w1:p1", herdr.StatusIdle)
	for i := 0; i < 8 && countKind(seen, EventSent) == 0; i++ {
		seen = append(seen, f.tick(time.Second)...)
	}
	if countKind(seen, EventSent) != 1 {
		t.Fatalf("a recovered seq read must let the message fire, got %v", seen)
	}
}

// A freshly started agent has not changed state yet, so its baseline really is 0.
// That must not be mistaken for "never armed".
func TestAfterCompletionFiresFromZeroBaseline(t *testing.T) {
	f := newFixture(t)
	// Added raw because AddPane always assigns a sequence; a just-started agent has
	// not changed state yet and legitimately reads 0.
	f.fake.Panes = append(f.fake.Panes, herdr.Pane{ID: "w1:p3", WorkspaceID: "w1",
		Agent: "qodercli", AgentStatus: herdr.StatusIdle})
	fresh := model.Target{Workspace: "w1", Pane: "w1:p3"}
	m := f.arm(f.newMsg("first transition\n", fresh, model.TriggerAfterCompletion))
	if !m.BaselineSet {
		t.Fatal("arming must record a baseline even when the sequence is still 0")
	}
	f.fake.SetStatus("w1:p3", herdr.StatusWorking)
	f.tick(time.Second)
	f.fake.SetStatus("w1:p3", herdr.StatusIdle)
	var evs []Event
	for i := 0; i < 8 && countKind(evs, EventSent) == 0; i++ {
		evs = append(evs, f.tick(time.Second)...)
	}
	if countKind(evs, EventSent) != 1 {
		t.Fatalf("a real transition from seq 0 must fire, got %v", evs)
	}
}

// Picking a trigger is the user's instruction. A herdr outage delays the baseline,
// it does not justify discarding the choice.
func TestSetTriggerSurvivesUnreachableHerdr(t *testing.T) {
	f := newFixture(t)
	m := f.newMsg("wait for herdr\n", model.Target{Workspace: "w1", Pane: "w1:p1"}, model.TriggerManual)
	f.fake.Unreachable = true
	if _, err := f.engine.SetTrigger(context.Background(), m.ID, model.TriggerAfterCompletion, nil); err != nil {
		t.Fatalf("set trigger during the outage: %v", err)
	}
	saved, err := f.st.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Trigger.Kind != model.TriggerAfterCompletion {
		t.Fatalf("trigger lost during the outage: %q", saved.Trigger.Kind)
	}
	if saved.Status != model.StatusWaiting {
		t.Fatalf("status = %s, want waiting", saved.Status)
	}
	if saved.BaselineSet {
		t.Fatal("recorded a baseline it cannot have read")
	}
	f.fake.Unreachable = false
	f.tick(time.Second)
	after, err := f.st.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.BaselineSet {
		t.Fatal("the first tick after herdr returns must record the missing baseline")
	}
}
