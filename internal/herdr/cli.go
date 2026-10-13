package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CLI talks to the running herdr server through the `herdr` socket CLI.
// Every command returns JSON, so no terminal scraping is involved.
type CLI struct {
	Bin         string
	Session     string
	ReadTimeout time.Duration
	SendTimeout time.Duration
}

// DefaultBin resolves the herdr executable, honouring HERDR_BIN.
func DefaultBin() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("HERDR_BIN")); custom != "" {
		return custom, nil
	}
	path, err := exec.LookPath("herdr")
	if err != nil {
		return "", fmt.Errorf("%w: herdr not found in PATH (set HERDR_BIN)", ErrUnavailable)
	}
	return path, nil
}

func NewCLI() (*CLI, error) {
	bin, err := DefaultBin()
	if err != nil {
		return nil, err
	}
	return &CLI{Bin: bin, ReadTimeout: 15 * time.Second, SendTimeout: 60 * time.Second}, nil
}

func (c *CLI) Name() string { return "herdr-cli" }

func (c *CLI) baseArgs() []string {
	if strings.TrimSpace(c.Session) == "" {
		return nil
	}
	return []string{"--session", c.Session}
}

// call runs herdr and decodes the success envelope into out. out may be nil.
// Herdr reports server errors as JSON on stderr with a non-zero exit status.
func (c *CLI) call(ctx context.Context, timeout time.Duration, args ...string) (json.RawMessage, error) {
	if timeout <= 0 {
		timeout = c.ReadTimeout
	}
	if c.Bin == "" {
		return nil, fmt.Errorf("%w: no herdr binary configured", ErrUnavailable)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdArgs := append(c.baseArgs(), args...)
	cmd := exec.CommandContext(runCtx, c.Bin, cmdArgs...)
	cmd.Env = c.herdrChildEnv()
	setExecProcAttr(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("herdr %s timed out after %s", strings.Join(args, " "), timeout)
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		// The process never started, so herdr was never asked: it is missing or not
		// executable. Callers treat that as "no state available right now" rather
		// than as a failed request, so it has to carry the sentinel.
		return nil, fmt.Errorf("%w: %s: %v", ErrUnavailable, c.Bin, err)
	}
	if err != nil {
		code, msg := parseCLIError(stderr.Bytes(), stdout.Bytes())
		if code == "" && msg == "" {
			msg = strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
		}
		if exitErr.ExitCode() == 2 {
			return nil, &CLIError{Code: "cli_usage", Message: msg}
		}
		if code == "" && msg == "" {
			msg = err.Error()
		}
		return nil, &CLIError{Code: code, Message: msg}
	}
	return unwrapResponse(bytes.TrimSpace(stdout.Bytes()), args)
}

// unwrapResponse strips the `{"id":…,"result":…}` envelope herdr prints. Most
// commands use it; `session list --json` is the exception and answers with its
// payload bare, so the envelope is detected by shape rather than assumed. Reading
// the roster as an envelope is what made every session collapse into one: the
// unwrap failed, the caller fell back to the ambient session, and nothing said so.
func unwrapResponse(body []byte, args []string) (json.RawMessage, error) {
	if len(body) == 0 {
		// `pane run` confirms with its exit status alone and prints no envelope.
		return nil, nil
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(body, &shape); err != nil {
		return nil, fmt.Errorf("herdr %s: unparseable response: %w", strings.Join(args, " "), err)
	}
	if result, ok := shape["result"]; ok {
		return result, nil
	}
	if _, ok := shape["id"]; ok {
		// An envelope with no result: herdr answered, but there was nothing in it.
		return nil, &CLIError{Code: "empty_result", Message: "herdr returned no result"}
	}
	return body, nil
}

func parseCLIError(blobs ...[]byte) (string, string) {
	for _, b := range blobs {
		b = bytes.TrimSpace(b)
		if len(b) == 0 {
			continue
		}
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b, &payload); err == nil && (payload.Error.Code != "" || payload.Error.Message != "") {
			return payload.Error.Code, payload.Error.Message
		}
	}
	return "", ""
}

// herdrChildEnv drops the inherited pane identity when an explicit session is
// configured, so `--session` wins over the caller context of whatever pane
// launched the outbox.
func (c *CLI) herdrChildEnv() []string {
	if strings.TrimSpace(c.Session) == "" {
		return os.Environ()
	}
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "HERDR_ENV="),
			strings.HasPrefix(kv, "HERDR_PANE_ID="),
			strings.HasPrefix(kv, "HERDR_WORKSPACE_ID="),
			strings.HasPrefix(kv, "HERDR_TAB_ID="):
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (c *CLI) Probe(ctx context.Context) error {
	_, err := c.call(ctx, c.ReadTimeout, "workspace", "list")
	if err != nil && !IsUnavailable(err) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}

// Snapshot reads this CLI's session. `api snapshot` answers the whole topology —
// workspaces, tabs, panes, agents and the session's focused pane — in one call,
// replacing four subprocess round-trips. Older herdr builds without it fall back
// to the individual lists.
func (c *CLI) Snapshot(ctx context.Context) (*Snapshot, error) {
	return c.snapshotSession(ctx, c.Session)
}

func (c *CLI) snapshotSession(ctx context.Context, session string) (*Snapshot, error) {
	// A pointer distinguishes "this herdr answers api snapshot with the shape we
	// expect" from "it answered something else", so a legitimately empty session
	// (all workspaces closed) is not mistaken for an unsupported command.
	if raw, err := c.call(ctx, c.ReadTimeout, "api", "snapshot"); err == nil {
		var env struct {
			Snapshot *struct {
				Workspaces    []Workspace `json:"workspaces"`
				Tabs          []Tab       `json:"tabs"`
				Panes         []Pane      `json:"panes"`
				Agents        []Pane      `json:"agents"`
				FocusedPaneID string      `json:"focused_pane_id"`
			} `json:"snapshot"`
		}
		if uerr := json.Unmarshal(raw, &env); uerr == nil && env.Snapshot != nil {
			s := env.Snapshot
			snap := BuildSession(session, s.Workspaces, s.Tabs, s.Panes, s.Agents)
			// Primary is set before stampSeq, which keys its note by session.
			snap.Primary = session
			c.stampSeq(snap, s.Agents, s.Panes)
			entry := Session{Name: session, Running: true, FocusedPane: s.FocusedPaneID}
			snap.Sessions = []Session{entry}
			snap.Source = c.Bin
			return snap, nil
		}
	}
	return c.snapshotLists(ctx, session)
}

// stampSeq records why a session cannot supply state_change_seq. agents is the
// only read that carries it, and an empty agents list simply means the session
// hosts no agents — not a failure: the per-pane check already tells the user a
// pane has nothing to wait on.
func (c *CLI) stampSeq(snap *Snapshot, agents, panes []Pane) {
	seq := func(ps []Pane) bool {
		for _, p := range ps {
			if p.StateChangeSeq != 0 {
				return true
			}
		}
		return false
	}
	if len(agents) == 0 || seq(agents) || seq(panes) {
		return
	}
	if snap.SeqUnavailable == nil {
		snap.SeqUnavailable = map[string]string{}
	}
	snap.SeqUnavailable[snap.Primary] = "herdr 未返回代理状态序号"
}

// snapshotLists is the pre-`api snapshot` path: four reads, no focus field.
func (c *CLI) snapshotLists(ctx context.Context, session string) (*Snapshot, error) {
	wsRaw, err := c.call(ctx, c.ReadTimeout, "workspace", "list")
	if err != nil {
		return nil, err
	}
	var ws struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(wsRaw, &ws); err != nil {
		return nil, err
	}

	// `tab list` only feeds the tab level of the tree; Build derives tabs from
	// the panes when it is missing, so a failure here is not worth surfacing.
	var tabs []Tab
	if tabRaw, terr := c.call(ctx, c.ReadTimeout, "tab", "list"); terr == nil {
		var ts struct {
			Tabs []Tab `json:"tabs"`
		}
		if uerr := json.Unmarshal(tabRaw, &ts); uerr == nil {
			tabs = ts.Tabs
		}
	}
	paneRaw, err := c.call(ctx, c.ReadTimeout, "pane", "list")
	if err != nil {
		return nil, err
	}
	var ps struct {
		Panes []Pane `json:"panes"`
	}
	if err := json.Unmarshal(paneRaw, &ps); err != nil {
		return nil, err
	}

	// agent list is the only read carrying state_change_seq, which is what proves
	// a completion actually happened. Older herdr versions may not expose it, so a
	// failure here is not fatal — but it has to be visible, or every armed message
	// silently waits for an edge that can never arrive.
	var agents []Pane
	agentRaw, aerr := c.call(ctx, c.ReadTimeout, "agent", "list")
	if aerr != nil {
		aerr = fmt.Errorf("agent list: %w", aerr)
	}
	if aerr == nil {
		var as struct {
			Agents []Pane `json:"agents"`
		}
		if uerr := json.Unmarshal(agentRaw, &as); uerr == nil {
			agents = as.Agents
		} else {
			aerr = fmt.Errorf("agent list: %w", uerr)
		}
	}

	snap := BuildSession(session, ws.Workspaces, tabs, ps.Panes, agents)
	snap.Primary = session
	snap.Sessions = []Session{{Name: session, Running: true}}
	if aerr != nil {
		snap.SeqUnavailable = map[string]string{session: aerr.Error()}
	} else {
		c.stampSeq(snap, agents, ps.Panes)
	}
	snap.Source = c.Bin
	return snap, nil
}

func (c *CLI) Send(ctx context.Context, paneID, text string) (SendResult, error) {
	if strings.TrimSpace(paneID) == "" {
		return SendResult{}, errors.New("no pane to send to")
	}
	if strings.TrimSpace(text) == "" {
		return SendResult{}, errors.New("message body is empty")
	}
	timeout := c.SendTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	_, err := c.call(ctx, timeout, "agent", "prompt", paneID, text)
	switch {
	case err == nil:
		return SendResult{Surface: "agent"}, nil
	case Blocked(err):
		return SendResult{}, fmt.Errorf("agent is waiting for approval in %s: answer it first", paneID)
	case Stalled(err):
		return SendResult{Surface: "agent", Warning: "herdr reported the agent did not start within its observation window"}, nil
	case NoAgent(err):
		// No agent in this pane: type into the terminal instead. herdr reports the
		// same agent_not_found for a closed pane, so let `pane run` tell them apart.
		if _, perr := c.call(ctx, timeout, "pane", "run", paneID, text); perr != nil {
			if PaneMissing(perr) {
				return SendResult{}, fmt.Errorf("pane %s was closed", paneID)
			}
			return SendResult{}, fmt.Errorf("pane run on %s failed: %w", paneID, perr)
		}
		return SendResult{Surface: "pane"}, nil
	default:
		return SendResult{}, err
	}
}

// Focus raises the tab owning the pane, which is how herdr marks unseen
// background work as seen. Only agent panes are addressable by id.
func (c *CLI) Focus(ctx context.Context, paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return errors.New("no pane to focus")
	}
	_, err := c.call(ctx, c.ReadTimeout, "agent", "focus", paneID)
	if err != nil && NoAgent(err) {
		return fmt.Errorf("pane %s has no agent to focus", paneID)
	}
	return err
}

// ReadPane reads recent terminal output from a pane.
func (c *CLI) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	if strings.TrimSpace(paneID) == "" {
		return "", errors.New("no pane to read")
	}
	if lines <= 0 {
		lines = 30
	}
	if c.Bin == "" {
		return "", fmt.Errorf("%w: no herdr binary configured", ErrUnavailable)
	}
	runCtx, cancel := context.WithTimeout(ctx, c.ReadTimeout)
	defer cancel()

	cmdArgs := append(c.baseArgs(), "pane", "read", paneID, "--lines", strconv.Itoa(lines), "--format", "text")
	cmd := exec.CommandContext(runCtx, c.Bin, cmdArgs...)
	cmd.Env = c.herdrChildEnv()
	setExecProcAttr(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("herdr pane read timed out after %s", c.ReadTimeout)
	}
	if err != nil {
		code, msg := parseCLIError(stderr.Bytes(), stdout.Bytes())
		if code == "" && msg == "" {
			msg = strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
		}
		return "", &CLIError{Code: code, Message: msg}
	}
	return stdout.String(), nil
}
