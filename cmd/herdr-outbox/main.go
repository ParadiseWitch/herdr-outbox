// Command herdr-outbox is a keyboard driven outbox for messages destined for the
// agents herdr is running.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/client"
	"herdr-outbox/internal/config"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/server"
	"herdr-outbox/internal/store"
	"herdr-outbox/internal/ui"
)

// var, not const: `make build` stamps it with -X main.version.
var version = "0.1.0"

type globalOptions struct {
	Dir      string
	Session  string
	Bin      string
	DryRun   bool
	Interval time.Duration
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-outbox: "+err.Error())
		os.Exit(1)
	}
}

const usageText = `herdr-outbox — herdr 代理的消息队列 / 发件箱

用法:
  herdr-outbox server              启动后台服务
  herdr-outbox server stop         停止后台服务
  herdr-outbox server status       查看服务状态
  herdr-outbox                     打开 TUI（需要服务运行中）
  herdr-outbox ls [--json]         列出消息
  herdr-outbox new [文本…]         创建草稿
  herdr-outbox send <id|all-ready> 立即发送
  herdr-outbox arm <id> <触发>     manual | completion | 20m | 2026-09-17 02:00
  herdr-outbox target <id> <面板>  指定目标面板 (wK:p1, qodercli-1)
  herdr-outbox log [n]             查看调度日志
  herdr-outbox panes               列出 herdr 工作区和面板
  herdr-outbox doctor              检查 herdr 连接和路径
  herdr-outbox config              查看配置
  herdr-outbox config editor <模式>  builtin (TUI 内编辑) | external ($EDITOR)
  herdr-outbox update              更新到最新版本
  herdr-outbox uninstall [--data]  卸载程序
  herdr-outbox version             显示版本 (--version, -V)

选项:
  --dir PATH          消息目录 (或 $HERDR_OUTBOX_DIR)
  --session NAME      herdr 会话
  --bin PATH          herdr 可执行文件 (或 $HERDR_BIN)
  --dry-run           使用内存模拟 herdr；不实际发送
  --interval DURATION 调度间隔，默认 1s
`

func run(argv []string) error {
	var opts globalOptions
	tail := parseGlobalFlags(argv, &opts)

	dir, err := resolveDir(opts.Dir)
	if err != nil {
		return err
	}

	cmd := "tui"
	args := tail
	if len(tail) > 0 {
		cmd, args = tail[0], tail[1:]
	}

	switch cmd {
	case "server":
		return runServer(dir, args, opts)
	case "tui", "":
		return runTUIViaClient(dir, opts)
	case "ls", "list":
		return runList(dir, args, opts)
	case "new":
		return runNew(dir, args, opts)
	case "send":
		return runSend(dir, args, opts)
	case "arm", "trigger":
		return runArm(dir, args, opts)
	case "target":
		return runTarget(dir, args, opts)
	case "log":
		return runLog(dir, args, opts)
	case "doctor":
		return runDoctor(dir, opts)
	case "config":
		return runConfig(dir, args)
	case "help", "--help", "-h":
		fmt.Print(usageText)
		return nil
	case "panes":
		return runPanes(dir, opts)
	case "update":
		return runUpdate(opts)
	case "uninstall":
		return runUninstall(dir, args)
	case "version", "--version", "-V":
		fmt.Println("herdr-outbox " + version)
		return nil
	default:
		fmt.Print(usageText)
		return fmt.Errorf("未知命令 %q", cmd)
	}
}

var knownCommands = map[string]bool{
	"server": true,
	"tui":    true, "ls": true, "list": true, "new": true,
	"send": true, "arm": true, "trigger": true, "target": true, "log": true,
	"doctor": true, "help": true, "version": true, "panes": true,
	// Listed so the flag parser does not eat them before the dispatch below.
	"--version": true, "-V": true, "--help": true, "-h": true,
	"update": true, "uninstall": true, "config": true,
}

func parseGlobalFlags(argv []string, opts *globalOptions) []string {
	var (
		cmd   string
		strip = argv
	)
	for i, a := range argv {
		if knownCommands[a] {
			cmd = a
			strip = append(append([]string{}, argv[:i]...), argv[i+1:]...)
			break
		}
	}
	fs := newFlagSet(opts)
	if err := fs.Parse(strip); err != nil {
		os.Exit(2)
	}
	rest := fs.Args()
	if cmd == "" {
		return rest
	}
	return append([]string{cmd}, rest...)
}

func newFlagSet(opts *globalOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("herdr-outbox", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Dir, "dir", "", "message directory")
	fs.StringVar(&opts.Session, "session", "", "herdr session")
	fs.StringVar(&opts.Bin, "bin", "", "herdr executable")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "use the in-memory fake backend")
	fs.DurationVar(&opts.Interval, "interval", time.Second, "scheduler tick")
	fs.Usage = func() { fmt.Print(usageText) }
	return fs
}

func resolveDir(flagDir string) (string, error) {
	if strings.TrimSpace(flagDir) != "" {
		return filepath.Abs(flagDir)
	}
	return store.DefaultDir()
}

func buildBackend(opts globalOptions) (herdr.Backend, error) {
	if opts.DryRun {
		fake := herdr.NewFake()
		fake.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", Agent: "qodercli",
			AgentStatus: herdr.StatusIdle, Cwd: cwdOrEmpty()})
		fake.Workspaces = []herdr.Workspace{{ID: "w1", Label: "dryrun", Focused: true}}
		fake.Panes[0].Focused = true
		return fake, nil
	}
	bin := opts.Bin
	if bin == "" {
		resolved, err := herdr.DefaultBin()
		if err != nil {
			return nil, fmt.Errorf("%w\n启动 herdr，或传递 --dry-run 使用模拟模式", err)
		}
		bin = resolved
	}
	cli := &herdr.CLI{Bin: bin, Session: opts.Session, ReadTimeout: 15 * time.Second, SendTimeout: 60 * time.Second}
	return cli, nil
}

func cwdOrEmpty() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// runServer starts the background server process.
func runServer(dir string, args []string, opts globalOptions) error {
	if len(args) > 0 {
		switch args[0] {
		case "stop":
			return serverStop(dir)
		case "status":
			return serverStatus(dir)
		case "help", "--help", "-h":
			fmt.Println("用法: herdr-outbox server [stop|status]")
			return nil
		default:
			return fmt.Errorf("未知 server 子命令 %q", args[0])
		}
	}

	if api.IsRunning(dir) {
		return fmt.Errorf("服务已在运行中；使用 `herdr-outbox server stop` 停止")
	}

	backend, err := buildBackend(opts)
	if err != nil {
		return err
	}
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	ledger, err := scheduler.OpenLedger(dir)
	if err != nil {
		return err
	}
	engine := scheduler.NewEngine(st, backend, ledger)

	srv := server.New(st, engine, ledger, dir, opts.Interval)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("herdr-outbox 服务已启动 (%s)，后端 %s\n", srv.Addr(), backend.Name())

	return srv.Wait(ctx)
}

func serverStop(dir string) error {
	if err := api.Stop(dir); err != nil {
		return fmt.Errorf("停止失败: %w", err)
	}
	fmt.Println("服务已停止")
	return nil
}

func serverStatus(dir string) error {
	base, err := api.BaseURL(dir)
	if err != nil {
		fmt.Println("服务未运行")
		return nil
	}
	if !api.IsRunning(dir) {
		fmt.Println("服务未响应")
		return nil
	}
	var status api.ServerStatus
	req, err := http.NewRequest("GET", base+"/api/status", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("服务未响应")
		return nil
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return err
	}
	fmt.Printf("服务运行中 %s\n", base)
	fmt.Printf("  后端:   %s\n", status.Backend)
	fmt.Printf("  目录:   %s\n", status.Dir)
	fmt.Printf("  运行:   %s\n", status.Uptime)
	fmt.Printf("  消息:   %d\n", status.Messages)
	return nil
}

// connectClient returns an HTTP client if the server is running, or nil.
func connectClient(dir string) *client.Client {
	base, err := api.BaseURL(dir)
	if err != nil {
		return nil
	}
	if !api.IsRunning(dir) {
		return nil
	}
	return client.New(base)
}

func requireClient(dir string) (*client.Client, error) {
	c := connectClient(dir)
	if c == nil {
		return nil, fmt.Errorf("服务未运行；请先执行 `herdr-outbox server`")
	}
	return c, nil
}

func runTUIViaClient(dir string, opts globalOptions) error {
	if err := ensureServer(dir, opts); err != nil {
		return err
	}
	c, err := requireClient(dir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	m := ui.New(ui.Options{Provider: c, Poll: opts.Interval, Editor: cfg.ResolveEditor()})
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return err
	}
	if m.Quitting() {
		fmt.Println("消息目录: " + dir)
	}
	return nil
}

// ensureServer starts a background server if none is running.
// This mirrors how herdr works: one command handles both server and client.
func ensureServer(dir string, opts globalOptions) error {
	if api.IsRunning(dir) {
		return nil
	}
	// The child process starts inside this directory and writes server.log into
	// it, so spawning first and letting the server create the dir fails: on
	// Windows CreateProcess reports "The directory name is invalid".
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建消息目录: %w", err)
	}
	if err := startBackgroundServer(dir, opts); err != nil {
		return fmt.Errorf("自动启动服务失败: %w", err)
	}
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if api.IsRunning(dir) {
			return nil
		}
	}
	return fmt.Errorf("服务启动超时")
}

// startBackgroundServer spawns a detached `herdr-outbox server` process.
func startBackgroundServer(dir string, opts globalOptions) error {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	args := []string{"--dir", dir, "server"}
	if opts.DryRun {
		args = append([]string{"--dry-run"}, args...)
	}
	if opts.Session != "" {
		args = append([]string{"--session", opts.Session}, args...)
	}
	if opts.Bin != "" {
		args = append([]string{"--bin", opts.Bin}, args...)
	}
	if opts.Interval != 0 && opts.Interval != time.Second {
		args = append([]string{"--interval", opts.Interval.String()}, args...)
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	detachProcess(cmd)
	// Redirect all standard streams to prevent any console attachment.
	cmd.Stdin = nil
	// Redirect output to a log file so the background server doesn't pollute
	// the TUI's terminal.
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		// If we can't create a log file, discard output to prevent console creation.
		devNull, _ := os.Open(os.DevNull)
		if devNull != nil {
			cmd.Stdout = devNull
			cmd.Stderr = devNull
		}
	}
	return cmd.Start()
}

func runList(dir string, args []string, opts globalOptions) error {
	asJSON := len(args) > 0 && (args[0] == "--json" || args[0] == "json")

	if c := connectClient(dir); c != nil {
		return listViaClient(c, asJSON)
	}
	return listLocal(dir, asJSON, opts)
}

func listViaClient(c *client.Client, asJSON bool) error {
	msgs, err := c.ListMessages(context.Background())
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(msgs)
	}
	printMessageList(msgs, nil)
	return nil
}

func listLocal(dir string, asJSON bool, opts globalOptions) error {
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	msgs, err := st.List()
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(msgs)
	}
	printMessageList(msgs, nil)
	return nil
}

func printMessageList(msgs []*model.Message, snap *herdr.Snapshot) {
	if len(msgs) == 0 {
		fmt.Println("发件箱为空；运行 `herdr-outbox new \"...\"`")
		return
	}
	for _, m := range msgs {
		line := fmt.Sprintf("%s %-8s %-24s %s", statusMark(m.Status), m.Status,
			truncate(m.Summary(), 24), m.Target.Display())
		switch m.Trigger.Kind {
		case model.TriggerScheduled:
			if m.Trigger.SendAt != nil {
				line += "  在 " + m.Trigger.SendAt.Format("2006-01-02 15:04")
			}
		case model.TriggerAfterCompletion:
			if snap != nil {
				if p, ok := snap.Resolve(m.Target); ok {
					line += "  代理 " + p.AgentStatus
				} else {
					line += "  目标已消失"
				}
			}
		}
		if m.SentAt != nil {
			line += "  发送于 " + m.SentAt.Format("15:04:05")
		}
		fmt.Printf("%-72s %s\n", line, m.ID)
	}
}

func statusMark(s model.Status) string {
	switch s {
	case model.StatusSent:
		return "✔"
	case model.StatusFailed:
		return "✘"
	case model.StatusWaiting:
		return "◎"
	case model.StatusScheduled:
		return "◷"
	case model.StatusPending:
		return "●"
	default:
		return "○"
	}
}

func runNew(dir string, args []string, opts globalOptions) error {
	c, err := requireClient(dir)
	if err != nil {
		return err
	}
	content := strings.TrimSpace(strings.Join(args, " "))
	if content == "" {
		content = "(从 CLI 创建；打开 " + dir + " 编辑)"
	}
	m, err := c.CreateMessage(context.Background(), &model.Message{
		Content: content,
		Trigger: model.Trigger{Kind: model.TriggerManual, SettleSeconds: 3},
		Status:  model.StatusDraft,
	})
	if err != nil {
		return err
	}
	// Only the id goes to stdout, so `ID=$(herdr-outbox new "...")` composes.
	fmt.Println(m.ID)
	fmt.Fprintf(os.Stderr, "草稿已创建；用 `herdr-outbox target %s <面板>` 指定目标\n", m.ID)
	return nil
}

func runSend(dir string, args []string, opts globalOptions) error {
	c, err := requireClient(dir)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("send 需要消息 id，或 `all-ready`")
	}
	ids := args
	if args[0] == "all-ready" {
		msgs, err := c.ListMessages(context.Background())
		if err != nil {
			return err
		}
		ids = nil
		for _, m := range msgs {
			if m.Status == model.StatusPending {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) == 0 {
			fmt.Println("没有待发送的消息")
			return nil
		}
	}
	ctx := context.Background()
	failed := false
	for _, id := range ids {
		events, err := c.SendNow(ctx, id)
		for _, ev := range events {
			fmt.Println(ev.String())
		}
		if err != nil {
			fmt.Println("  错误: " + err.Error())
			failed = true
		}
	}
	if failed {
		return errors.New("一条或多条消息发送失败")
	}
	return nil
}

func runArm(dir string, args []string, opts globalOptions) error {
	c, err := requireClient(dir)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return errors.New("arm 需要 <id> <触发方式>")
	}
	id, spec := args[0], strings.Join(args[1:], " ")
	ctx := context.Background()
	switch strings.ToLower(spec) {
	case "manual", "now", "m":
		if _, err := c.SetTrigger(ctx, id, model.TriggerManual, nil); err != nil {
			return err
		}
		fmt.Println(id + " 已设为手动发送")
		return nil
	case "completion", "after_completion", "complete":
		m, err := c.SetTrigger(ctx, id, model.TriggerAfterCompletion, nil)
		if err != nil {
			return err
		}
		baseline := "未记录基线，herdr 恢复后会自动补上"
		if m.BaselineSet {
			baseline = fmt.Sprintf("基线序号 %d", m.BaselineSeq)
		}
		fmt.Printf("%s 等待 %s (%s)\n", id, m.Target.Display(), baseline)
		return nil
	}
	at, err := model.ParseSendAt(model.Now(), spec)
	if err != nil {
		return err
	}
	m, err := c.SetTrigger(ctx, id, model.TriggerScheduled, at)
	if err != nil {
		return err
	}
	fmt.Printf("%s 将在 %s 发送到 %s\n", id, m.Trigger.SendAt.Format("2006-01-02 15:04:05"), m.Target.Display())
	return nil
}

func runTarget(dir string, args []string, opts globalOptions) error {
	c, err := requireClient(dir)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return errors.New("target 需要 <id> <面板>")
	}
	id, pane := args[0], args[1]
	ctx := context.Background()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return err
	}
	found, ok := snap.Resolve(model.Target{Pane: pane})
	if !ok {
		return fmt.Errorf("没有面板匹配 %q；运行 `herdr-outbox doctor` 查看活跃面板", pane)
	}
	m, err := c.SetTarget(ctx, id, found.Target())
	if err != nil {
		return err
	}
	fmt.Printf("%s → %s (%s, 代理 %s)\n", id, m.Target.Display(), found.ID, orShell(found.Agent))
	return nil
}

func orShell(s string) string {
	if s == "" {
		return "shell"
	}
	return s
}

func runLog(dir string, args []string, opts globalOptions) error {
	n := 20
	if len(args) > 0 {
		if _, err := fmt.Sscanf(args[0], "%d", &n); err != nil {
			return fmt.Errorf("log 需要数字，得到 %q", args[0])
		}
	}

	if c := connectClient(dir); c != nil {
		recs, path, err := c.LogRecords(context.Background(), n)
		if err != nil {
			return err
		}
		printLogRecords(recs, path)
		return nil
	}

	ledger, err := scheduler.OpenLedger(dir)
	if err != nil {
		return err
	}
	recs := ledger.Tail(n)
	printLogRecords(recs, ledger.Path())
	return nil
}

func printLogRecords(recs []scheduler.Record, path string) {
	if len(recs) == 0 {
		fmt.Println("在 " + path + " 中没有发送记录")
		return
	}
	for _, r := range recs {
		fmt.Printf("%s %-9s %-10s %-24s %s\n", r.TS.Format("15:04:05"), r.Phase, r.Pane, truncate(r.ID, 22), r.Reason)
	}
}

func runPanes(dir string, opts globalOptions) error {
	backend, err := buildBackend(opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	snap, err := backend.Snapshot(ctx)
	if err != nil {
		return err
	}
	printWorkspacesAndPanes(snap)
	return nil
}

func printWorkspacesAndPanes(snap *herdr.Snapshot) {
	for _, w := range snap.Workspaces {
		focus := ""
		if w.Focused {
			focus = " (focused)"
		}
		fmt.Printf("  %-14s %s%s\n", w.ID, w.Display(), focus)
	}
	for _, p := range snap.Panes {
		agent := p.Agent
		if agent == "" {
			agent = "-"
		}
		status := p.AgentStatus
		if status == "" {
			status = "shell"
		}
		name := p.Ordinal
		if name == "" {
			name = "-"
		}
		fmt.Printf("    %-10s %-12s %-10s %-8s %s\n", p.ID, name, agent, status, truncate(p.Title(), 44))
	}
}

func runDoctor(dir string, opts globalOptions) error {
	fmt.Println("herdr-outbox " + version)
	fmt.Println("消息: " + dir)

	backend, err := buildBackend(opts)
	if err != nil {
		return err
	}
	fmt.Println("后端:   " + backend.Name())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := backend.Probe(ctx); err != nil {
		return fmt.Errorf("herdr 探测失败: %w", err)
	}
	snap, err := backend.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("快照失败: %w", err)
	}
	fmt.Printf("herdr:    %d 个工作区, %d 个面板\n", len(snap.Workspaces), len(snap.Panes))
	if snap.AgentSeqError != "" {
		// No state_change_seq means no completion can ever be detected, which is the
		// first thing a stuck after_completion message needs explained.
		fmt.Println("警告:     读不到代理状态序号 (" + snap.AgentSeqError + ")；\"等待完成\"不会触发")
	}
	for _, w := range snap.Workspaces {
		focus := ""
		if w.Focused {
			focus = " (当前)"
		}
		fmt.Printf("  %-14s %s%s\n", w.ID, w.Display(), focus)
	}
	for _, p := range snap.Panes {
		agent := p.Agent
		if agent == "" {
			agent = "-"
		}
		status := p.AgentStatus
		if status == "" {
			status = "shell"
		}
		fmt.Printf("    %-10s %-10s %-8s %s\n", p.ID, agent, status, truncate(p.Title(), 44))
	}

	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	msgs, err := st.List()
	if err != nil {
		return err
	}
	var waiting, scheduled int
	for _, m := range msgs {
		switch m.Status {
		case model.StatusWaiting:
			waiting++
		case model.StatusScheduled:
			scheduled++
		}
	}
	fmt.Printf("发件箱:   %d 条消息, %d 条等待完成, %d 条定时\n", len(msgs), waiting, scheduled)

	if api.IsRunning(dir) {
		fmt.Println("服务:     运行中")
	} else {
		fmt.Println("服务:     未运行")
	}
	if opts.DryRun {
		fmt.Println("注意:     --dry-run 已启用；不会发送到真实代理")
	}
	return nil
}

func runConfig(dir string, args []string) error {
	if len(args) == 0 {
		cfg, err := config.Load(dir)
		if err != nil {
			return err
		}
		fmt.Println("配置: " + config.Path(dir))
		fmt.Printf("  editor:  %-9s (%s)\n", cfg.ResolveEditor(), editorModeHint(cfg.ResolveEditor()))
		return nil
	}
	if len(args) == 1 && args[0] == "editor" {
		cfg, err := config.Load(dir)
		if err != nil {
			return err
		}
		fmt.Println(string(cfg.ResolveEditor()))
		return nil
	}
	if args[0] != "editor" || len(args) < 2 {
		return fmt.Errorf("用法: herdr-outbox config [editor <builtin|external>]")
	}
	mode := config.EditorMode(strings.ToLower(strings.TrimSpace(args[1])))
	switch mode {
	case config.EditorBuiltin, config.EditorExternal:
	default:
		return fmt.Errorf("editor 只能是 builtin 或 external，得到 %q", args[1])
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	cfg.Editor = mode
	if err := config.Write(dir, cfg); err != nil {
		return err
	}
	fmt.Printf("editor = %s → %s\n", mode, editorModeHint(mode))
	fmt.Println("已写入 " + config.Path(dir))
	return nil
}

func editorModeHint(mode config.EditorMode) string {
	if mode == config.EditorExternal {
		return "使用 $EDITOR 外部编辑器"
	}
	return "在 TUI 内编辑"
}

func runUpdate(opts globalOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法找到当前可执行文件: %w", err)
	}
	fmt.Printf("当前路径: %s\n", exe)
	fmt.Printf("当前版本: %s\n", version)

	if _, err := exec.LookPath("git"); err == nil {
		if dir, err := findSourceDir(exe); err == nil {
			fmt.Println("检测到源码目录，从源码更新…")
			return updateFromSource(dir)
		}
	}

	if _, err := exec.LookPath("go"); err == nil {
		fmt.Println("使用 go install 更新…")
		cmd := exec.Command("go", "install", "herdr-outbox/cmd/herdr-outbox@latest")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	return fmt.Errorf("未找到 git 或 go，无法自动更新")
}

func findSourceDir(exe string) (string, error) {
	dir := filepath.Dir(exe)
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("source directory not found")
}

func updateFromSource(dir string) error {
	pull := exec.Command("git", "-C", dir, "pull", "--ff-only")
	pull.Stdout = os.Stdout
	pull.Stderr = os.Stderr
	if err := pull.Run(); err != nil {
		return fmt.Errorf("git pull 失败: %w", err)
	}
	build := exec.Command("go", "build", "-o", "herdr-outbox"+exeSuffix(), "./cmd/herdr-outbox")
	build.Dir = dir
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("go build 失败: %w", err)
	}
	fmt.Println("更新完成")
	return nil
}

func exeSuffix() string {
	if strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), ".exe") {
		return ".exe"
	}
	return ""
}

func runUninstall(dir string, args []string) error {
	removeData := false
	for _, a := range args {
		if a == "--data" || a == "-data" {
			removeData = true
		}
	}

	if api.IsRunning(dir) {
		fmt.Println("正在停止服务…")
		api.Stop(dir)
		time.Sleep(500 * time.Millisecond)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法找到可执行文件: %w", err)
	}

	if err := os.Remove(exe); err != nil {
		fmt.Fprintf(os.Stderr, "删除 %s 失败: %v\n", exe, err)
		fmt.Fprintln(os.Stderr, "请手动删除该文件")
	} else {
		fmt.Println("已删除 " + exe)
	}

	if removeData {
		if _, err := os.Stat(dir); err == nil {
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintf(os.Stderr, "删除数据目录 %s 失败: %v\n", dir, err)
			} else {
				fmt.Println("已删除数据目录 " + dir)
			}
		}
	} else {
		fmt.Println("数据目录已保留: " + dir)
		fmt.Println("如需完全清除，请运行: herdr-outbox uninstall --data")
	}

	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
