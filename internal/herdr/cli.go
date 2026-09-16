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
	if err != nil {
		code, msg := parseCLIError(stderr.Bytes(), stdout.Bytes())
		if code == "" && msg == "" {
			msg = strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
		}
		if exitErr := new(exec.ExitError); errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return nil, &CLIError{Code: "cli_usage", Message: msg}
		}
		if code == "" && msg == "" {
			msg = err.Error()
		}
		return nil, &CLIError{Code: code, Message: msg}
	}
	if len(bytes.TrimSpace(stdout.Bytes())) == 0 {
		// `pane run` confirms with its exit status alone and prints no envelope.
		return nil, nil
	}
	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if uerr := json.Unmarshal(stdout.Bytes(), &envelope); uerr != nil {
		return nil, fmt.Errorf("herdr %s: unparseable response: %w", strings.Join(args, " "), uerr)
	}
	if len(envelope.Result) == 0 {
		return nil, &CLIError{Code: "empty_result", Message: "herdr returned no result"}
	}
	return envelope.Result, nil
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
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (c *CLI) Snapshot(ctx context.Context) (*Snapshot, error) {
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

	// agent list adds state_change_seq, which pane list omits. Older herdr
	// versions may not expose it, so a failure here is not fatal.
	var agents []Pane
	if agentRaw, aerr := c.call(ctx, c.ReadTimeout, "agent", "list"); aerr == nil {
		var as struct {
			Agents []Pane `json:"agents"`
		}
		if uerr := json.Unmarshal(agentRaw, &as); uerr == nil {
			agents = as.Agents
		}
	}

	snap := Build(ws.Workspaces, ps.Panes, agents)
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
		if code == "pane_not_found" {
			return "", &CLIError{Code: code, Message: msg}
		}
		return "", &CLIError{Code: code, Message: msg}
	}
	return stdout.String(), nil
}

