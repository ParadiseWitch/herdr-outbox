package herdr

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"herdr-outbox/internal/model"
)

// Captured from a live herdr 0.9.0 server so the decoders are pinned to the real
// field names rather than to assumptions.
const workspaceListJSON = `{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[
{"active_tab_id":"w9:t1","agent_status":"idle","focused":false,"label":"app","number":1,"pane_count":6,"tab_count":2,"workspace_id":"w9"},
{"active_tab_id":"wK:t1","agent_status":"working","focused":true,"label":"herdr-outbox","number":3,"pane_count":2,"tab_count":1,"workspace_id":"wK"}]}}`

const paneListJSON = `{"id":"cli:pane:list","result":{"panes":[
{"agent":"qodercli","agent_session":{"agent":"qodercli","kind":"id","source":"herdr:qodercli","value":"7f255ba3"},"agent_status":"working","cwd":"D:\\myproject\\herdr-outbox","focused":false,"pane_id":"wK:p1","revision":4,"tab_id":"wK:t1","terminal_id":"term_1","terminal_title":"实现 go bubbletea tui | Working","terminal_title_stripped":"实现 go bubbletea tui | Working","workspace_id":"wK"},
{"agent_status":"unknown","cwd":"D:\\myproject\\herdr-outbox","focused":true,"pane_id":"wK:p2","revision":1,"tab_id":"wK:t1","terminal_id":"term_2","terminal_title":"pwsh.EXE","terminal_title_stripped":"pwsh.EXE","workspace_id":"wK"},
{"agent":"claude","agent_status":"idle","cwd":"C:\\code\\x","focused":false,"pane_id":"wK:p10","revision":2,"tab_id":"wK:t1","terminal_id":"term_3","terminal_title":"claude","terminal_title_stripped":"claude","workspace_id":"wK"}]}}`

// pane list has no state_change_seq; agent list is the only source for it.
const agentListJSON = `{"id":"cli:agent:list","result":{"agents":[
{"agent":"qodercli","agent_session":{"agent":"qodercli","kind":"id","source":"herdr:qodercli","value":"7f255ba3"},"agent_status":"working","cwd":"D:\\myproject\\herdr-outbox","focused":false,"pane_id":"wK:p1","revision":4,"state_change_seq":223,"tab_id":"wK:t1","terminal_id":"term_1","terminal_title":"实现 go bubbletea tui | Working","terminal_title_stripped":"实现 go bubbletea tui | Working","workspace_id":"wK"},
{"agent":"claude","agent_status":"idle","cwd":"C:\\code\\x","focused":false,"pane_id":"wK:p10","revision":2,"state_change_seq":88,"tab_id":"wK:t1","terminal_id":"term_3","terminal_title":"claude","terminal_title_stripped":"claude","workspace_id":"wK"}]}}`

func resultOf(t *testing.T, payload string) json.RawMessage {
	t.Helper()
	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if len(envelope.Result) == 0 {
		t.Fatal("payload has no result")
	}
	return envelope.Result
}

func decodeSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	var ws struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(resultOf(t, workspaceListJSON), &ws); err != nil {
		t.Fatal(err)
	}
	var ps struct {
		Panes []Pane `json:"panes"`
	}
	if err := json.Unmarshal(resultOf(t, paneListJSON), &ps); err != nil {
		t.Fatal(err)
	}
	var as struct {
		Agents []Pane `json:"agents"`
	}
	if err := json.Unmarshal(resultOf(t, agentListJSON), &as); err != nil {
		t.Fatal(err)
	}
	return Build(ws.Workspaces, ps.Panes, as.Agents)
}

func TestDecodesLiveHerdrPayloads(t *testing.T) {
	snap := decodeSnapshot(t)
	if len(snap.Workspaces) != 2 {
		t.Fatalf("workspaces = %d", len(snap.Workspaces))
	}
	if snap.Workspaces[1].Display() != "herdr-outbox" {
		t.Fatalf("workspace label = %q", snap.Workspaces[1].Display())
	}
	if len(snap.Panes) != 3 {
		t.Fatalf("panes = %d", len(snap.Panes))
	}
	p1, ok := snap.Pane("wK:p1")
	if !ok {
		t.Fatal("wK:p1 missing")
	}
	if p1.AgentStatus != StatusWorking || p1.StateChangeSeq != 223 {
		t.Fatalf("agent merge failed: %+v", p1)
	}
	if p1.SessionID() != "7f255ba3" {
		t.Fatalf("session id = %q", p1.SessionID())
	}
	if p1.WsLabel != "herdr-outbox" {
		t.Fatalf("workspace label not joined: %+v", p1)
	}
	p2, _ := snap.Pane("wK:p2")
	if p2.HasAgent() {
		t.Fatalf("shell pane should have no agent: %+v", p2)
	}
	if p2.StateChangeSeq != 0 {
		t.Fatalf("shell pane picked up agent state: %+v", p2)
	}
}

func TestOrdinalsAreStableAndOrdered(t *testing.T) {
	snap := decodeSnapshot(t)
	seen := map[string]string{}
	for _, p := range snap.Panes {
		if p.Ordinal != "" {
			seen[p.Ordinal] = p.ID
		}
	}
	// wK:p10 sorts after wK:p2, so qodercli-1 is p1 and claude-1 is p10.
	if seen["qodercli-1"] != "wK:p1" {
		t.Fatalf("ordinals = %v", seen)
	}
	if seen["claude-1"] != "wK:p10" {
		t.Fatalf("ordinals = %v", seen)
	}
	if PaneSortKey("wK:p2") >= PaneSortKey("wK:p10") {
		t.Fatal("pane ids must sort numerically, not lexically")
	}
}

func TestResolveAcceptsStoredForms(t *testing.T) {
	snap := decodeSnapshot(t)
	cases := []struct {
		name   string
		target model.Target
		want   string
	}{
		{"pane id", model.Target{Workspace: "wK", Pane: "wK:p1"}, "wK:p1"},
		{"workspace label", model.Target{Workspace: "herdr-outbox", Pane: "wK:p1"}, "wK:p1"},
		{"bare pane suffix", model.Target{Workspace: "wK", Pane: "p1"}, "wK:p1"},
		{"ordinal", model.Target{Workspace: "wK", Pane: "qodercli-1"}, "wK:p1"},
		{"ordinal across workspaces", model.Target{Workspace: "wK", Pane: "claude-1"}, "wK:p10"},
		{"pane id only", model.Target{Pane: "wK:p2"}, "wK:p2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := snap.Resolve(tc.target)
			if !ok || got.ID != tc.want {
				t.Fatalf("resolve(%+v) = %+v, %v; want %s", tc.target, got, ok, tc.want)
			}
		})
	}
	// A target that no longer exists must be reported as unresolved, not guessed.
	if _, ok := snap.Resolve(model.Target{Workspace: "wK", Pane: "qodercli-9"}); ok {
		t.Fatal("invented ordinal resolved")
	}
	if _, ok := snap.Resolve(model.Target{Workspace: "nope", Pane: "wK:p1"}); ok {
		t.Fatal("wrong workspace accepted the pane")
	}
	if _, ok := snap.Resolve(model.Target{}); ok {
		t.Fatal("empty target resolved")
	}
}

func TestErrorClassification(t *testing.T) {
	blocked := &CLIError{Code: "agent_blocked", Message: "agent is waiting at a dialog"}
	if !Blocked(blocked) || Stalled(blocked) || NoAgent(blocked) {
		t.Fatalf("bad classification for %v", blocked)
	}
	stalled := &CLIError{Code: "agent_prompt_stalled", Message: "no lifecycle change"}
	if !Stalled(stalled) || Blocked(stalled) {
		t.Fatalf("bad classification for %v", stalled)
	}
	notFound := &CLIError{Code: "agent_not_found", Message: "agent target wK:p2 not found"}
	if !NoAgent(notFound) || Blocked(notFound) {
		t.Fatalf("bad classification for %v", notFound)
	}
	missing := &CLIError{Code: "pane_not_found", Message: "pane wK:p1 not found"}
	if !PaneMissing(missing) || NoAgent(missing) {
		t.Fatalf("bad classification for %v", missing)
	}
	if NoAgent(nil) || Blocked(nil) || Stalled(nil) || PaneMissing(nil) {
		t.Fatal("nil reported as an error")
	}
	if !strings.Contains(blocked.Error(), "agent_blocked") {
		t.Fatalf("error text = %q", blocked.Error())
	}
}

func TestParseCLIErrorFromStderrEnvelope(t *testing.T) {
	payload := []byte(`{"error":{"code":"agent_not_found","message":"agent target wK:p2 not found"},"id":"cli:agent:prompt"}`)
	code, msg := parseCLIError(payload, nil)
	if code != "agent_not_found" || msg == "" {
		t.Fatalf("code=%q msg=%q", code, msg)
	}
	if code, msg := parseCLIError([]byte("not json"), []byte("plain stderr")); code != "" || msg != "" {
		t.Fatalf("unexpected parse: %q %q", code, msg)
	}
}

func TestSettledOnlyCoversIdleAndDone(t *testing.T) {
	for _, s := range []string{StatusIdle, StatusDone} {
		if !Settled(s) {
			t.Fatalf("%s should be settled", s)
		}
	}
	for _, s := range []string{StatusWorking, StatusBlocked, StatusUnknown, ""} {
		if Settled(s) {
			t.Fatalf("%q must not be treated as completion", s)
		}
	}
}

// TestLiveHerdrServer exercises the real CLI when a server is reachable, so the
// decoders cannot silently drift from the installed herdr version.
func TestLiveHerdrServer(t *testing.T) {
	if _, err := DefaultBin(); err != nil {
		t.Skip("herdr not installed")
	}
	if strings.TrimSpace(os.Getenv("HERDR_ENV")) != "1" && strings.TrimSpace(os.Getenv("HERDR_LIVE_TEST")) != "1" {
		t.Skip("not running inside a herdr pane; set HERDR_LIVE_TEST=1 to force")
	}
	cli, err := NewCLI()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cli.Probe(ctx); err != nil {
		t.Skipf("no herdr server reachable: %v", err)
	}
	snap, err := cli.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Panes) == 0 {
		t.Fatal("live server reported no panes")
	}
	var withAgent int
	for _, p := range snap.Panes {
		if p.HasAgent() {
			withAgent++
			switch p.AgentStatus {
			case StatusIdle, StatusWorking, StatusBlocked, StatusDone, StatusUnknown:
			default:
				t.Fatalf("unexpected agent_status %q on %s", p.AgentStatus, p.ID)
			}
		}
	}
	t.Logf("live: %d workspaces, %d panes, %d agents", len(snap.Workspaces), len(snap.Panes), withAgent)
	// Sending is intentionally not exercised here; the TUI verification covers a
	// real send against a scratch pane the test creates itself.
}

func TestPaneIDSuffixesSortBesideTheirNumber(t *testing.T) {
	if PaneSortKey("w9:p1N") != PaneSortKey("w9:p1") {
		t.Fatalf("reused pane suffix sorts away from its number: %d vs %d",
			PaneSortKey("w9:p1N"), PaneSortKey("w9:p1"))
	}
	if PaneSortKey("w9:p2") >= PaneSortKey("w9:p10") {
		t.Fatal("lettered suffix broke numeric order")
	}
	if TabSortKey("w9:t2") >= TabSortKey("w9:t10") {
		t.Fatalf("tab ids sorted lexically: %d vs %d", TabSortKey("w9:t2"), TabSortKey("w9:t10"))
	}
	panes := []Pane{
		{ID: "w9:p3", WorkspaceID: "w9", TabID: "w9:t10"},
		{ID: "w9:p1", WorkspaceID: "w9", TabID: "w9:t2"},
		{ID: "w9:p1N", WorkspaceID: "w9", TabID: "w9:t2"},
		{ID: "w9:p2", WorkspaceID: "w9", TabID: "w9:t2"},
	}
	snap := Build(nil, panes, nil)
	var order []string
	for _, p := range snap.Panes {
		order = append(order, p.ID)
	}
	if strings.Join(order, " ") != "w9:p1 w9:p1N w9:p2 w9:p3" {
		t.Fatalf("order = %v", order)
	}
}
