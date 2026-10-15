# Per-Pane after_completion Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make multiple `after_completion` messages on the same pane fire one-per-completion in a user-set order, instead of all firing on the same agent-idle edge.

**Architecture:** A `QueuePos int` (1-based, per pane) on `model.Message` is the single ordering source. The scheduler computes each pane's head (lowest `QueuePos`) per tick and freezes non-heads (refreshing their baseline so the next one waits for the *next* completion edge). A failed head freezes the pane's chain. The store sorts by `QueuePos` so list order equals send order; the TUI shows a rank badge and `[`/`]` to reorder.

**Tech Stack:** Go 1.26, bubbletea TUI, file-based YAML store, net/http server + client.

**Spec:** `docs/superpowers/specs/2026-10-15-per-pane-completion-queue-design.md`

---

## File Structure

- `internal/model/message.go` — add `QueuePos` field (data only).
- `internal/store/store.go` — `Sort` orders same-pane after_completion by `QueuePos`.
- `internal/scheduler/engine.go` — `computeQueue`, head gate + freeze-refresh in `advanceCompletion`, `QueuePos` assign in `armFrom`, reset on `Sent`, `SetQueuePos` method.
- `internal/api/types.go` — `Message.QueuePos`, `QueuePosRequest`.
- `internal/server/handlers.go` + `internal/server/server.go` — carry `QueuePos`, add `PUT /api/messages/{id}/queue-pos`.
- `internal/client/client.go` — carry `QueuePos`, `SetQueuePos` method.
- `internal/dataprovider/dataprovider.go` — `SetQueuePos` on the interface.
- `internal/ui/localprovider.go` — `SetQueuePos` passthrough.
- `internal/ui/view.go` — rank badge in `listRows`.
- `internal/ui/keys.go` — `[`/`]` handlers.
- `internal/ui/scope_test.go` / `picker_test.go` — help/hint text if changed.

Tests live beside each package: `internal/store/store_test.go`, `internal/scheduler/engine_test.go`, `internal/server/server_test.go`, `internal/ui/queue_test.go` (new).

---

## Task 1: `QueuePos` field + store round-trip

**Files:**
- Modify: `internal/model/message.go` (after `SettleSince`, ~line 189)
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/store_test.go`:

```go
func TestQueuePosRoundTrips(t *testing.T) {
	s := openStore(t)
	m, err := s.Create(&model.Message{
		Content:  "x",
		Target:   model.Target{Session: "default", Workspace: "w1", Pane: "w1:p1"},
		Trigger:  model.Trigger{Kind: model.TriggerAfterCompletion},
		Status:   model.StatusWaiting,
		QueuePos: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.QueuePos != 3 {
		t.Fatalf("QueuePos lost across round trip: %d", got.QueuePos)
	}
}

// QueuePos 0 is the "not queued" sentinel and must not be written to disk, so
// manual/scheduled messages keep byte-identical files after this change.
func TestQueuePosOmittedWhenZero(t *testing.T) {
	s := openStore(t)
	m, err := s.Create(&model.Message{
		Content: "x", Target: model.Target{Pane: "w1:p1"},
		Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "queue_pos") {
		t.Fatalf("zero QueuePos leaked to disk:\n%s", raw)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestQueuePos -v`
Expected: FAIL — `unknown field QueuePos` (compile error).

- [ ] **Step 3: Add the field**

In `internal/model/message.go`, inside `type Message struct`, after the `SettleSince *time.Time ...` line (the completion bookkeeping block), add:

```go
	// QueuePos is a message's 1-based slot in its pane's after_completion queue.
	// 0 means "not in a queue": manual, scheduled, or never armed as
	// after_completion. Assigned once on first entry and kept across re-arms and
	// failures so a retried message returns to its original place. Lower fires
	// first; gaps are fine. Only consulted for after_completion messages.
	QueuePos int `yaml:"queue_pos,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestQueuePos -v`
Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
git add internal/model/message.go internal/store/store_test.go
git commit -m "Add QueuePos field for per-pane completion ordering"
```

---

## Task 2: `computeQueue` — per-pane head + frozen set

**Files:**
- Modify: `internal/scheduler/engine.go` (add near `advanceCompletion`, ~line 268)
- Test: `internal/scheduler/engine_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/scheduler/engine_test.go`. Uses the existing `newFixture`, `f.newMsg`, `f.arm`, `target`, and `herdr`/`model` imports already present:

```go
// computeQueue picks, per pane, the after_completion message with the lowest
// QueuePos as head, and flags a pane frozen when any of its after_completion
// messages is Failed (a delivered-later chain must not skip an undelivered head).
func TestComputeQueue(t *testing.T) {
	f := newFixture(t)
	// Two messages on w1:p1 (QueuePos 1, 2) and one on w1:p2.
	a := f.newMsg("a\n", target, model.TriggerAfterCompletion)
	a.QueuePos = 1
	b := f.newMsg("b\n", target, model.TriggerAfterCompletion)
	b.QueuePos = 2
	c := f.newMsg("c\n", model.Target{Workspace: "w1", Pane: "w1:p2"}, model.TriggerAfterCompletion)
	c.QueuePos = 1
	for _, m := range []*model.Message{a, b, c} {
		m.Status = model.StatusWaiting
		if err := f.st.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	msgs := []*model.Message{a, b, c}
	q := computeQueue(msgs)
	if q.head["w1:p1"] != a.ID {
		t.Fatalf("w1:p1 head = %q, want a (%q)", q.head["w1:p1"], a.ID)
	}
	if q.head["w1:p2"] != c.ID {
		t.Fatalf("w1:p2 head = %q, want c", q.head["w1:p2"])
	}
	if len(q.frozen) != 0 {
		t.Fatalf("nothing should be frozen: %v", q.frozen)
	}

	// Fail the head of w1:p1 -> that pane freezes, w1:p2 unaffected.
	a.Status = model.StatusFailed
	if err := f.st.Save(a); err != nil {
		t.Fatal(err)
	}
	q = computeQueue([]*model.Message{a, b, c})
	if !q.frozen["w1:p1"] {
		t.Fatal("w1:p1 should freeze while its head is failed")
	}
	if q.head["w1:p1"] != "" {
		t.Fatalf("frozen pane must have no head, got %q", q.head["w1:p1"])
	}
	if q.frozen["w1:p2"] {
		t.Fatal("w1:p2 wrongly frozen")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scheduler/ -run TestComputeQueue -v`
Expected: FAIL — `undefined: computeQueue`.

- [ ] **Step 3: Implement `computeQueue`**

Add to `internal/scheduler/engine.go` (above `advanceCompletion`). Pane identity is the bare `Target.Pane` here because the queue is always evaluated within one snapshot read where ids are already qualified by session upstream; to stay correct across sessions, key on the qualified id when a session is present:

```go
// queueState is this tick's per-pane after_completion ordering. head maps a pane
// to the id of the message that may fire (lowest QueuePos); frozen marks panes
// whose chain is halted because a message is Failed.
type queueState struct {
	head   map[string]string
	frozen map[string]bool
}

// queueKey is the pane a message is queued on: its qualified id so two sessions
// that both own `w1:p1` never share a queue.
func queueKey(m *model.Message) string {
	return herdr.Qualify(m.Target.Session, m.Target.Pane)
}

// queued reports whether this message participates in a completion queue.
func queued(m *model.Message) bool {
	return m.Trigger.Kind == model.TriggerAfterCompletion &&
		(m.Status == model.StatusWaiting || m.Status == model.StatusFailed)
}

func computeQueue(msgs []*model.Message) queueState {
	q := queueState{head: map[string]string{}, frozen: map[string]bool{}}
	for _, m := range msgs {
		if !queued(m) {
			continue
		}
		key := queueKey(m)
		if m.Status == model.StatusFailed {
			q.frozen[key] = true
			continue
		}
		// Waiting: lowest QueuePos wins; tie-break on CreatedAt then ID so the
		// choice is stable and matches the store's sort.
		if cur, ok := q.head[key]; ok {
			if !q.frozen[key] && betterQueuePos(m, msgsByID(msgs)[cur]) {
				q.head[key] = m.ID
			}
			continue
		}
		q.head[key] = m.ID
	}
	// A frozen pane has no head.
	for k := range q.frozen {
		delete(q.head, k)
	}
	return q
}

// betterQueuePos reports whether a should fire before b in the same pane.
func betterQueuePos(a, b *model.Message) bool {
	switch {
	case a.QueuePos != b.QueuePos:
		return a.QueuePos < b.QueuePos
	case !a.CreatedAt.Equal(b.CreatedAt):
		return a.CreatedAt.Before(b.CreatedAt)
	default:
		return a.ID < b.ID
	}
}

func msgsByID(msgs []*model.Message) map[string]*model.Message {
	m := make(map[string]*model.Message, len(msgs))
	for _, x := range msgs {
		m[x.ID] = x
	}
	return m
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/scheduler/ -run TestComputeQueue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler/engine.go internal/scheduler/engine_test.go
git commit -m "Add computeQueue for per-pane completion head selection"
```

---

## Task 3: Head gate + freeze-refresh in `advanceCompletion`

**Files:**
- Modify: `internal/scheduler/engine.go` (`Step` ~160; `advance` ~247; `advanceCompletion` ~275)
- Test: `internal/scheduler/engine_test.go`

- [ ] **Step 1: Write the failing test**

```go
// Two after_completion messages on one pane fire on SEPARATE completions: the
// first idle edge sends only the head; the second sends the successor. This is
// the whole point of the queue -- today both fire on one edge.
func TestCompletionQueueChainsAcrossEdges(t *testing.T) {
	f := newFixture(t)
	head := f.arm(f.newMsg("head\n", target, model.TriggerAfterCompletion))
	succ := f.arm(f.newMsg("succ\n", target, model.TriggerAfterCompletion))
	if head.QueuePos >= succ.QueuePos {
		t.Fatalf("head pos %d should precede succ %d", head.QueuePos, succ.QueuePos)
	}

	// First completion: only head fires.
	f.driveCompletion("w1:p1")
	if n := len(f.fake.Sends()); n != 1 {
		t.Fatalf("after first completion: %d sends, want 1", n)
	}
	if f.reload(head.ID).Status != model.StatusSent {
		t.Fatalf("head not sent: %v", f.reload(head.ID).Status)
	}
	if f.reload(succ.ID).Status != model.StatusWaiting {
		t.Fatalf("successor fired early: %v", f.reload(succ.ID).Status)
	}

	// Second completion: successor fires.
	f.driveCompletion("w1:p1")
	if n := len(f.fake.Sends()); n != 2 {
		t.Fatalf("after second completion: %d sends, want 2", n)
	}
	if f.reload(succ.ID).Status != model.StatusSent {
		t.Fatalf("successor not sent: %v", f.reload(succ.ID).Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scheduler/ -run TestCompletionQueueChainsAcrossEdges -v`
Expected: FAIL — both send on the first completion (`2 sends, want 1`), and/or `QueuePos` is 0/0 (Task 4 assigns it; if it fails on the `QueuePos` precondition first, that is acceptable — Task 4 makes this test's ordering assertion meaningful).

- [ ] **Step 3: Thread `queueState` through the tick**

In `Step` (`engine.go` ~160), after `now := e.clock()` and before the `for _, m := range msgs` loop, compute the queue and pass it down:

```go
	q := computeQueue(msgs)
	var events []Event
	for _, m := range msgs {
		evs, err := e.advance(ctx, m, snap, now, q)
		events = append(events, evs...)
		if err != nil {
			events = append(events, Event{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(), Detail: err.Error(), At: now})
		}
	}
	return events, nil
```

Change `advance`'s signature to accept `q queueState` and forward it:

```go
func (e *Engine) advance(ctx context.Context, m *model.Message, snap *herdr.Snapshot, now time.Time, q queueState) ([]Event, error) {
	switch m.Status {
	case model.StatusScheduled:
		// ... unchanged ...
	case model.StatusWaiting:
		return e.advanceCompletion(ctx, m, snap, now, q)
	default:
		return nil, nil
	}
}
```

- [ ] **Step 4: Add the gate in `advanceCompletion`**

Change its signature to `func (e *Engine) advanceCompletion(ctx context.Context, m *model.Message, snap *herdr.Snapshot, now time.Time, q queueState) ([]Event, error)`. Immediately AFTER the existing `pane, ok := snap.Resolve(m.Target)` + `if !ok {...}` + `e.bindTarget(m, pane)` block (engine.go ~282-286), insert the gate:

```go
	// Only the pane's head may fire. A frozen pane (a Failed after_completion on
	// it) halts the whole chain until the user retries or removes the failure.
	key := queueKey(m)
	if q.frozen[key] {
		return nil, nil
	}
	if q.head[key] != m.ID {
		// Frozen successor: keep its baseline pinned to the current seq so that
		// when it becomes head it waits for the NEXT completion edge rather than
		// reusing the one that fired its predecessor.
		if m.BaselineSeq != pane.StateChangeSeq || !m.BaselineSet || m.ObservedWorking || m.SettleSince != nil {
			m.BaselineSeq = pane.StateChangeSeq
			m.BaselineSet = true
			m.ObservedWorking = false
			m.SettleSince = nil
			return nil, e.saveState(m)
		}
		return nil, nil
	}
```

The rest of `advanceCompletion` (the `switch pane.AgentStatus` block) is unchanged — it now only runs for the head.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/scheduler/ -run TestCompletionQueueChainsAcrossEdges -v`
Expected: PASS (after Task 4 assigns distinct `QueuePos`; if still failing on the pos precondition, complete Task 4 then re-run — both tasks land together in practice, but keep commits separate).

- [ ] **Step 6: Commit**

```bash
git add internal/scheduler/engine.go internal/scheduler/engine_test.go
git commit -m "Gate completion sends to the per-pane head; freeze successors"
```

---

## Task 4: Assign `QueuePos` on arm; reset on Sent / non-completion

**Files:**
- Modify: `internal/scheduler/engine.go` (`armFrom` ~424; `dispatchLocked` ~581; `SetTrigger` ~450; `SetTarget` ~478)
- Test: `internal/scheduler/engine_test.go`

- [ ] **Step 1: Write the failing test**

```go
// First arm of an after_completion message on a pane takes the next slot; a
// re-arm keeps its slot so a retried head returns to its original place.
func TestQueuePosAssignment(t *testing.T) {
	f := newFixture(t)
	a := f.arm(f.newMsg("a\n", target, model.TriggerAfterCompletion))
	b := f.arm(f.newMsg("b\n", target, model.TriggerAfterCompletion))
	if a.QueuePos != 1 || b.QueuePos != 2 {
		t.Fatalf("assignment: a=%d b=%d, want 1,2", a.QueuePos, b.QueuePos)
	}
	// Re-arm b (e.g. target re-set): position is preserved.
	if _, err := f.engine.SetTarget(context.Background(), b.ID, b.Target); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(b.ID).QueuePos; got != 2 {
		t.Fatalf("re-arm changed position: %d, want 2", got)
	}
	// A manual message never gets a slot.
	m := f.newMsg("m\n", target, model.TriggerManual)
	if _, err := f.engine.Arm(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if m.QueuePos != 0 {
		t.Fatalf("manual message got QueuePos %d, want 0", m.QueuePos)
	}
}

// Sending a queued message clears its slot so the pane's queue compacts on the
// next completion; a failed send keeps the slot (the chain stays frozen on it).
func TestQueuePosClearedOnSent(t *testing.T) {
	f := newFixture(t)
	a := f.arm(f.newMsg("a\n", target, model.TriggerAfterCompletion))
	if _, err := f.engine.SendNow(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(a.ID); got.QueuePos != 0 || got.Status != model.StatusSent {
		t.Fatalf("after send: pos=%d status=%v, want 0/sent", got.QueuePos, got.Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scheduler/ -run 'TestQueuePos(Assignment|ClearedOnSent)' -v`
Expected: FAIL — `QueuePos` is always 0 (never assigned).

- [ ] **Step 3: Assign in `armFrom`**

In `armFrom` (`engine.go` ~443), replace the final baseline-setting block so it also manages `QueuePos`. Insert before `m.BaselineSeq = pane.StateChangeSeq`:

```go
	// Manage queue membership. Only after_completion messages hold a slot; a
	// message that changes trigger kind or pane drops and re-takes one, but a
	// re-arm of the same queued message keeps its original slot.
	if m.Trigger.Kind == model.TriggerAfterCompletion {
		if m.QueuePos == 0 {
			m.QueuePos = e.nextQueuePos(queueKey(m))
		}
	} else {
		m.QueuePos = 0
	}
```

Add the helper (near `computeQueue`):

```go
// nextQueuePos is one past the highest slot currently in use on a pane, among
// messages still waiting or failed. Reads the live store so concurrent arms in
// one tick each get a distinct slot.
func (e *Engine) nextQueuePos(key string) int {
	msgs, err := e.Store.List()
	if err != nil {
		return 1
	}
	max := 0
	for _, m := range msgs {
		if queued(m) && queueKey(m) == key && m.QueuePos > max {
			max = m.QueuePos
		}
	}
	return max + 1
}
```

- [ ] **Step 4: Reset on pane change in `SetTarget`**

`SetTarget` re-arms; if the pane changed, the old slot is meaningless. Before `if _, err := e.Arm(ctx, m); ...` (engine.go ~487), add:

```go
	if queueKey(m) != queueKeyOfTarget(t) {
		m.QueuePos = 0 // moved to a different pane: take a fresh slot on arm
	}
```

Add beside `queueKey`:

```go
func queueKeyOfTarget(t model.Target) string { return herdr.Qualify(t.Session, t.Pane) }
```

- [ ] **Step 5: Clear on Sent in `dispatchLocked`**

In `dispatchLocked` at the `m.Status = model.StatusSent` block (engine.go ~581), after `m.SettleSince = nil`, add:

```go
	m.QueuePos = 0 // delivered: leave the queue so the pane compacts
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/scheduler/ -run 'TestQueuePos|TestCompletionQueue' -v`
Expected: PASS (all four scheduler queue tests).

- [ ] **Step 7: Commit**

```bash
git add internal/scheduler/engine.go internal/scheduler/engine_test.go
git commit -m "Assign and clear QueuePos across arm, send, and re-target"
```

---

## Task 5: Failed head freezes the chain

**Files:**
- Modify: `internal/scheduler/engine.go` (`finishFailed` — keep `QueuePos`)
- Test: `internal/scheduler/engine_test.go`

- [ ] **Step 1: Write the failing test**

The existing `herdr.Fake` has a `FailSend error` field to force a send error.

```go
// A failed head halts the pane's chain: the successor must NOT auto-advance past
// an undelivered predecessor. Retrying the head with s resumes it.
func TestFailedHeadFreezesChain(t *testing.T) {
	f := newFixture(t)
	head := f.arm(f.newMsg("head\n", target, model.TriggerAfterCompletion))
	succ := f.arm(f.newMsg("succ\n", target, model.TriggerAfterCompletion))

	f.fake.FailSend = errors.New("boom")
	f.driveCompletion("w1:p1") // head tries to send and fails
	if got := f.reload(head.ID); got.Status != model.StatusFailed {
		t.Fatalf("head status = %v, want failed", got.Status)
	}
	if got := f.reload(head.ID); got.QueuePos == 0 {
		t.Fatal("a failed head must keep its slot")
	}
	if got := f.reload(succ.ID); got.Status != model.StatusWaiting {
		t.Fatalf("successor advanced past a failed head: %v", got.Status)
	}

	// Recover: allow sends, retry the head explicitly; on success the chain resumes.
	f.fake.FailSend = nil
	if _, err := f.engine.SendNow(context.Background(), head.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(head.ID); got.Status != model.StatusSent {
		t.Fatalf("head after retry = %v, want sent", got.Status)
	}
	f.driveCompletion("w1:p1")
	if got := f.reload(succ.ID); got.Status != model.StatusSent {
		t.Fatalf("successor never resumed after head recovered: %v", got.Status)
	}
}
```

Add `import "errors"` to the test file if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scheduler/ -run TestFailedHeadFreezesChain -v`
Expected: FAIL — either the successor advanced (freeze not honored because `finishFailed` cleared the slot, or `computeQueue` didn't see it as failed), or the head lost its `QueuePos`.

- [ ] **Step 3: Confirm `finishFailed` keeps `QueuePos`**

`finishFailed` (engine.go ~593) sets `m.Status = model.StatusFailed` but must NOT touch `QueuePos`. Verify by reading it; if any line zeroes `QueuePos`, remove it. No change expected — this step locks the invariant.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/scheduler/ -run TestFailedHeadFreezesChain -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler/engine.go internal/scheduler/engine_test.go
git commit -m "Freeze a pane's completion chain while its head is failed"
```

---

## Task 6: `SetQueuePos` in the engine (swap with neighbor)

**Files:**
- Modify: `internal/scheduler/engine.go` (new method after `SetTarget`)
- Test: `internal/scheduler/engine_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSetQueuePosSwapsWithinPane(t *testing.T) {
	f := newFixture(t)
	a := f.arm(f.newMsg("a\n", target, model.TriggerAfterCompletion)) // pos 1
	b := f.arm(f.newMsg("b\n", target, model.TriggerAfterCompletion)) // pos 2
	c := f.arm(f.newMsg("c\n", target, model.TriggerAfterCompletion)) // pos 3

	if _, err := f.engine.SetQueuePos(context.Background(), c.ID, -1); err != nil {
		t.Fatal(err)
	}
	// c moved up one: order is now a, c, b.
	if f.reload(c.ID).QueuePos != 2 || f.reload(b.ID).QueuePos != 3 {
		t.Fatalf("swap down failed: c=%d b=%d", f.reload(c.ID).QueuePos, f.reload(b.ID).QueuePos)
	}
	// Head cannot move further up: no-op, no error.
	if _, err := f.engine.SetQueuePos(context.Background(), a.ID, -1); err != nil {
		t.Fatal(err)
	}
	if f.reload(a.ID).QueuePos != 1 {
		t.Fatalf("head moved past the top: %d", f.reload(a.ID).QueuePos)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scheduler/ -run TestSetQueuePosSwapsWithinPane -v`
Expected: FAIL — `f.engine.SetQueuePos undefined`.

- [ ] **Step 3: Implement `SetQueuePos`**

Add after `SetTarget` in `engine.go`:

```go
// SetQueuePos moves a message one slot up (delta -1) or down (delta +1) within
// its pane's completion queue by swapping slots with the neighbor. Out of range,
// or a message that is not queued, is a no-op.
func (e *Engine) SetQueuePos(ctx context.Context, id string, delta int) (*model.Message, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if m.Trigger.Kind != model.TriggerAfterCompletion ||
		(m.Status != model.StatusWaiting && m.Status != model.StatusFailed) {
		return m, nil
	}
	peers, err := e.queuedPeers(queueKey(m))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(peers, func(i, j int) bool { return betterQueuePos(peers[i], peers[j]) })
	i := indexOfID(peers, id)
	j := i + delta
	if i < 0 || j < 0 || j >= len(peers) {
		return m, nil // at an end: nothing to swap with
	}
	peers[i], peers[j] = peers[j], peers[i]
	// Re-assign contiguous slots in the new order, skipping any gaps so the queue
	// stays dense after a swap.
	for rank, p := range peers {
		p.QueuePos = rank + 1
	}
	if err := e.Store.Save(peers[i]); err != nil {
		return nil, err
	}
	if err := e.Store.Save(peers[j]); err != nil {
		return nil, err
	}
	return e.Store.Get(id)
}

// queuedPeers returns every waiting-or-failed after_completion message on the
// same pane, including the caller.
func (e *Engine) queuedPeers(key string) ([]*model.Message, error) {
	msgs, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	var out []*model.Message
	for _, m := range msgs {
		if queued(m) && queueKey(m) == key {
			out = append(out, m)
		}
	}
	return out, nil
}

func indexOfID(msgs []*model.Message, id string) int {
	for i, m := range msgs {
		if m.ID == id {
			return i
		}
	}
	return -1
}
```

Add `"sort"` to the engine imports if absent.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/scheduler/ -run TestSetQueuePosSwapsWithinPane -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler/engine.go internal/scheduler/engine_test.go
git commit -m "Add SetQueuePos to reorder a message within its pane queue"
```

---

## Task 7: Store sort orders same-pane after_completion by `QueuePos`

**Files:**
- Modify: `internal/store/store.go` (`Sort`, ~170)
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
// Within the same status rank, after_completion messages on one pane sort by
// QueuePos, so the list order equals the send order.
func TestSortOrdersQueuedByQueuePos(t *testing.T) {
	base := time.Now()
	msgs := []*model.Message{
		{ID: "c", Status: model.StatusWaiting, Trigger: model.Trigger{Kind: model.TriggerAfterCompletion}, Target: model.Target{Pane: "w1:p1"}, QueuePos: 3, CreatedAt: base},
		{ID: "a", Status: model.StatusWaiting, Trigger: model.Trigger{Kind: model.TriggerAfterCompletion}, Target: model.Target{Pane: "w1:p1"}, QueuePos: 1, CreatedAt: base},
		{ID: "b", Status: model.StatusWaiting, Trigger: model.Trigger{Kind: model.TriggerAfterCompletion}, Target: model.Target{Pane: "w1:p1"}, QueuePos: 2, CreatedAt: base},
	}
	Sort(msgs)
	if msgs[0].ID != "a" || msgs[1].ID != "b" || msgs[2].ID != "c" {
		t.Fatalf("order = %s %s %s, want a b c", msgs[0].ID, msgs[1].ID, msgs[2].ID)
	}
}
```

`Sort` is package-level in `store.go` and `store_test.go` is in `package store`, so the test calls `Sort(msgs)` directly.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSortOrdersQueuedByQueuePos -v`
Expected: FAIL — order is by `ID`/time, not `QueuePos` (c/a/b or a/c/b depending on tiebreak).

- [ ] **Step 3: Add the `QueuePos` clause to `Sort`**

In `store.go` `Sort`, after the scheduled-`SendAt` comparison and before the `UpdatedAt` fallback, add a clause for same-pane after_completion:

```go
		if a.Trigger.Kind == model.TriggerAfterCompletion && b.Trigger.Kind == model.TriggerAfterCompletion &&
			a.Target.Pane == b.Target.Pane && a.QueuePos != b.QueuePos {
			return a.QueuePos < b.QueuePos
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSortOrdersQueuedByQueuePos -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "Sort same-pane completion messages by QueuePos"
```

---

## Task 8: Carry `QueuePos` over the wire + `queue-pos` endpoint

**Files:**
- Modify: `internal/api/types.go`, `internal/server/handlers.go`, `internal/server/server.go`, `internal/client/client.go`
- Test: `internal/server/server_test.go`

- [ ] **Step 1: Write the failing test**

```go
// QueuePos must survive the server->client round trip, or a remote TUI would
// reorder a pane's queue differently from the engine that sends it.
func TestQueuePosSurvivesTheRoundTrip(t *testing.T) {
	srv, ctx, _, _ := newTestServer(t)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := client.New("http://" + srv.Addr())
	created, err := c.CreateMessage(context.Background(), &model.Message{
		Content: "x",
		Target:  model.Target{Session: "default", Workspace: "w1", Pane: "w1:p1"},
		Trigger: model.Trigger{Kind: model.TriggerAfterCompletion},
		Status:  model.StatusWaiting,
		QueuePos: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.QueuePos != 4 {
		t.Fatalf("QueuePos after create = %d, want 4", created.QueuePos)
	}
	list, err := c.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].QueuePos != 4 {
		t.Fatalf("QueuePos lost on list: %+v", list)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestQueuePosSurvivesTheRoundTrip -v`
Expected: FAIL — `QueuePos` is 0 (not carried).

- [ ] **Step 3: Add `QueuePos` to `api.Message` and both mappers**

`internal/api/types.go`, in `type Message struct`, after `SettleSince *time.Time ...`:

```go
	QueuePos int `json:"queue_pos,omitempty"`
```

`internal/server/handlers.go` `toAPIMessage`: add `QueuePos: m.QueuePos,`. `fromAPIMessage`: add `QueuePos: a.QueuePos,`.
`internal/client/client.go` `toAPIMessage`: add `QueuePos: m.QueuePos,`. `fromAPIMessage`: add `QueuePos: a.QueuePos,`.

- [ ] **Step 4: Add the `queue-pos` request type, route, handler, client method**

`internal/api/types.go`, after `TargetRequest`:

```go
type QueuePosRequest struct {
	Delta int `json:"delta"`
}
```

`internal/server/server.go`, after the `/target` route (line ~144):

```go
	mux.HandleFunc("PUT /api/messages/{id}/queue-pos", s.handleSetQueuePos)
```

`internal/server/handlers.go`, after `handleSetTarget`:

```go
func (s *Server) handleSetQueuePos(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req api.QueuePosRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := s.engine.SetQueuePos(r.Context(), id, req.Delta)
	if err != nil {
		writeBackendError(w, err)
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_updated", MessageID: m.ID, Title: m.Summary(), At: time.Now()})
	writeJSON(w, http.StatusOK, toAPIMessage(m))
}
```

`internal/client/client.go`, after `SetTarget`:

```go
func (c *Client) SetQueuePos(ctx context.Context, id string, delta int) (*model.Message, error) {
	req := api.QueuePosRequest{Delta: delta}
	var resp api.Message
	if err := c.put(ctx, "/api/messages/"+id+"/queue-pos", req, &resp); err != nil {
		return nil, err
	}
	return fromAPIMessage(&resp), nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestQueuePosSurvivesTheRoundTrip -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/types.go internal/server/handlers.go internal/server/server.go internal/client/client.go internal/server/server_test.go
git commit -m "Carry QueuePos over the wire and add the queue-pos endpoint"
```

---

## Task 9: `SetQueuePos` on the `DataProvider` interface

**Files:**
- Modify: `internal/dataprovider/dataprovider.go`, `internal/ui/localprovider.go`
- Test: (compile-time — the interface assertion below)

- [ ] **Step 1: Add to the interface**

`internal/dataprovider/dataprovider.go`, in `DataProvider`, after `SetTarget`:

```go
	SetQueuePos(ctx context.Context, id string, delta int) (*model.Message, error)
```

- [ ] **Step 2: Implement on `LocalProvider`**

`internal/ui/localprovider.go`, after `SetTarget`:

```go
func (p *LocalProvider) SetQueuePos(ctx context.Context, id string, delta int) (*model.Message, error) {
	return p.Engine.SetQueuePos(ctx, id, delta)
}
```

- [ ] **Step 3: Verify both providers satisfy the interface**

`Client` already has `SetQueuePos` (Task 8). Run:

```bash
go build ./...
```
Expected: builds clean. If a test fake implements `DataProvider`, add a `SetQueuePos` stub there too (search: `grep -rl "SetTarget(ctx" internal/ui/*_test.go`).

- [ ] **Step 4: Commit**

```bash
git add internal/dataprovider/dataprovider.go internal/ui/localprovider.go
git commit -m "Expose SetQueuePos through the DataProvider interface"
```

---

## Task 10: Rank badge in the list

**Files:**
- Modify: `internal/ui/view.go` (`listRows` ~219)
- Test: `internal/ui/queue_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/ui/queue_test.go`:

```go
package ui

import (
	"testing"

	"herdr-outbox/internal/model"
)

func TestQueueRank(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	a := putQueued(t, m, "a", 1)
	b := putQueued(t, m, "b", 2)
	man := &model.Message{Content: "m", Target: model.Target{Workspace: "w1", Pane: "w1:p1"},
		Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusPending}
	if err := m.provider.(*LocalProvider).Store.Create(man); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	if r := m.queueRank(a); r != 1 {
		t.Fatalf("a rank = %d, want 1", r)
	}
	if r := m.queueRank(b); r != 2 {
		t.Fatalf("b rank = %d, want 2", r)
	}
	for _, msg := range m.msgs {
		if msg.ID == man.ID && m.queueRank(msg) != 0 {
			t.Fatal("manual message got a rank")
		}
	}
}
```

Add a helper in the same file:

```go
func putQueued(t *testing.T, m *Model, body string, pos int) *model.Message {
	t.Helper()
	msg := &model.Message{Content: body,
		Target:   model.Target{Workspace: "w1", Pane: "w1:p1"},
		Trigger:  model.Trigger{Kind: model.TriggerAfterCompletion},
		Status:   model.StatusWaiting,
		QueuePos: pos}
	created, err := m.provider.(*LocalProvider).Store.Create(msg)
	if err != nil {
		t.Fatal(err)
	}
	return created
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestQueueRank -v`
Expected: FAIL — `m.queueRank undefined`.

- [ ] **Step 3: Implement `queueRank` + badge**

In `view.go`, add:

```go
// queueRank is a message's 1-based position in its pane's completion queue, or 0
// if it is not queued. It ranks by QueuePos so the badge matches send order.
func (m *Model) queueRank(msg *model.Message) int {
	if msg.Trigger.Kind != model.TriggerAfterCompletion ||
		(msg.Status != model.StatusWaiting && msg.Status != model.StatusFailed) {
		return 0
	}
	key := queueKeyOf(msg.Target)
	rank := 1
	for _, other := range m.msgs {
		if other.ID == msg.ID {
			continue
		}
		if other.Trigger.Kind == model.TriggerAfterCompletion &&
			(other.Status == model.StatusWaiting || other.Status == model.StatusFailed) &&
			queueKeyOf(other.Target) == key && beforeInQueue(other, msg) {
			rank++
		}
	}
	return rank
}
```

The scheduler's `queueKey`/`betterQueuePos` are unexported, so `view.go` gets its own tiny copies (the ui package already imports `herdr`):

```go
func queueKeyOf(t model.Target) string { return herdr.Qualify(t.Session, t.Pane) }

func beforeInQueue(a, b *model.Message) bool {
	switch {
	case a.QueuePos != b.QueuePos:
		return a.QueuePos < b.QueuePos
	case !a.CreatedAt.Equal(b.CreatedAt):
		return a.CreatedAt.Before(b.CreatedAt)
	default:
		return a.ID < b.ID
	}
}
```

Then in `listRows`, after `glyph := msg.Status.Glyph()`, prepend the badge:

```go
	badge := " "
	if r := m.queueRank(msg); r > 0 {
		badge = circled(r)
	}
```

and change the row build from `row := " " + glyph + fav + " " + title` to:

```go
	row := " " + badge + glyph + fav + " " + title
```

Add the circled-number helper:

```go
// circled renders 1..20 as ①..⑳; larger ranks fall back to "#N". All are
// single runes measured by the same path the row is padded with, so title and
// meta lines stay aligned.
func circled(n int) string {
	if n >= 1 && n <= 20 {
		return string(rune(0x2460 + n - 1))
	}
	return "#" + strconv.Itoa(n)
}
```

Add `"strconv"` to `view.go` imports if absent.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestQueueRank -v`
Expected: PASS.

- [ ] **Step 5: Verify frame alignment still passes**

Run: `go test ./internal/ui/ -run 'TestFrameFits|TestPanelsAreDivided' -v`
Expected: PASS. If it fails, the circled glyph width disagrees with `fit` — switch `circled` to always return `"#N"` and re-run.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/view.go internal/ui/queue_test.go
git commit -m "Show a per-pane queue rank badge in the list"
```

---

## Task 11: `[` / `]` reorder keys

**Files:**
- Modify: `internal/ui/keys.go` (browse `switch key`, near `f`/`y`/`d`)
- Test: `internal/ui/queue_test.go`

- [ ] **Step 1: Write the failing test**

Append to `queue_test.go`:

```go
func TestBracketReordersQueue(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	a := putQueued(t, m, "a", 1)
	b := putQueued(t, m, "b", 2)
	m.reload(t)

	// Cursor on b (pos 2), press [ to move it up past a.
	m.cursor = indexOfVisible(m, b.ID)
	pressRune(t, m, '[')
	m.reload(t)
	if f := findMsg(m, a.ID); f.QueuePos != 2 {
		t.Fatalf("a pos = %d, want 2 after moving b up", f.QueuePos)
	}
	if f := findMsg(m, b.ID); f.QueuePos != 1 {
		t.Fatalf("b pos = %d, want 1", f.QueuePos)
	}
}

func indexOfVisible(m *Model, id string) int {
	for i, msg := range m.visible() {
		if msg.ID == id {
			return i
		}
	}
	return 0
}

func findMsg(m *Model, id string) *model.Message {
	for _, msg := range m.msgs {
		if msg.ID == id {
			return msg
		}
	}
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestBracketReordersQueue -v`
Expected: FAIL — `[` does nothing (no handler).

- [ ] **Step 3: Add the handlers**

In `keys.go` browse `switch key`, add near the `f`/`y`/`d` cases:

```go
	case "[":
		if sel != nil {
			return m.reorder(sel, -1)
		}
		return m, nil
	case "]":
		if sel != nil {
			return m.reorder(sel, 1)
		}
		return m, nil
```

Add the method:

```go
// reorder nudges a queued message up or down its pane's completion queue and
// keeps the cursor on it so repeated presses walk it to the target slot.
func (m *Model) reorder(sel *model.Message, delta int) (tea.Model, tea.Cmd) {
	updated, err := m.provider.SetQueuePos(context.Background(), sel.ID, delta)
	if err != nil {
		m.setNotice("调整顺序失败：" + err.Error())
		return m, m.loadMsg()
	}
	m.selectedID = updated.ID
	return m, m.loadMsg()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -run TestBracketReordersQueue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/keys.go internal/ui/queue_test.go
git commit -m "Add [ and ] to reorder a message within its pane queue"
```

---

## Task 12: Help text + final gate

**Files:**
- Modify: `internal/ui/view.go` (`helpView`)
- Test: `go test ./...`

- [ ] **Step 1: Document the reorder keys**

In `helpView`'s "编辑" section, after the `{"u", "存为草稿"}` row, add:

```go
			{"[ / ]", "队列中：上移 / 下移本面板发送顺序"},
```

- [ ] **Step 2: Run the full suite**

Run: `go test ./...`
Expected: PASS, all packages.

- [ ] **Step 3: Vet + fmt**

Run: `go vet ./... && gofmt -l internal/ cmd/`
Expected: no output.

- [ ] **Step 4: Live smoke (optional, if a herdr server is up)**

Run: `herdr-outbox server stop; make build; ./herdr-outbox.exe server &` then open the TUI, create two `after_completion` messages on one pane, confirm badges `①②`, press `[`/`]`, and watch the order swap. Not automatable here (no pty); do manually.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/view.go
git commit -m "Document queue reorder keys in help"
```

---

## Self-Review Notes

- **Spec coverage:** chain (T3), assign/clear (T4), freeze-on-fail (T5), reorder (T6/T11), list==send order (T7), wire (T8/T9), badge (T10), help (T12). All spec sections have a task.
- **Type consistency:** `QueuePos int` everywhere; `queueKey`/`queueKeyOf`/`queueKeyOfTarget` are per-package copies of `herdr.Qualify(Session, Pane)` — verify they agree on the session-empty case (all use `Target.Session`, which `bindTarget` populates, so a queued message always has a session). If a legacy message has empty `Session`, all three fall back to the bare pane id consistently.
- **Placeholders:** none. Every code step is complete.
- **Risk:** circled-number width vs `fit`. Task 10 Step 5 has an explicit fallback to `#N`.
