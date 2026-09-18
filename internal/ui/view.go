package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"herdr-outbox/internal/config"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
)

var (
	colAccent  = lipgloss.Color("39")
	colDim     = lipgloss.Color("243")
	colOk      = lipgloss.Color("42")
	colWarn    = lipgloss.Color("214")
	colErr     = lipgloss.Color("203")
	colWorking = lipgloss.Color("220")
	colBlock   = lipgloss.Color("213")

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stHeading  = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stDim      = lipgloss.NewStyle().Foreground(colDim)
	stSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("238"))
	stPanel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim)
	stAlert = lipgloss.NewStyle().Foreground(colErr)
	stOk    = lipgloss.NewStyle().Foreground(colOk)
	stWarn  = lipgloss.NewStyle().Foreground(colWarn)
)

func (m *Model) View() string {
	if !m.ready {
		return "正在启动 herdr-outbox…"
	}
	if m.width < 40 || m.height < 8 {
		return fit("窗口太小；请放大终端", m.width)
	}

	bodyHeight := m.height - 4
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	leftW := m.leftWidth()
	rightW := m.width - leftW - 3
	if rightW < 12 {
		rightW = 12
	}

	header := m.headerView(m.width)
	notice := m.noticeView(m.width)
	status := m.statusView(m.width)

	body := m.overlayView(m.width, bodyHeight)
	if body == "" {
		// The divider has to be a full-height column: a single-line "│" block gets
		// padded with blanks and the two panels appear unjoined below the header.
		divider := strings.TrimRight(strings.Repeat("│\n", bodyHeight), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.listView(leftW, bodyHeight), divider, m.detailPanel(rightW, bodyHeight))
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, notice, status)
}

func (m *Model) leftWidth() int {
	w := m.width * 32 / 100
	if w < 26 {
		w = 26
	}
	if w > 52 {
		w = 52
	}
	if w > m.width-16 {
		w = m.width - 16
	}
	return w
}

func (m *Model) detailHeight() int {
	h := m.height - 6
	if h < 5 {
		return 5
	}
	return h
}

func (m *Model) headerView(width int) string {
	var counts struct{ open, sent, armed int }
	for _, msg := range m.msgs {
		switch {
		case msg.Status == model.StatusSent:
			counts.sent++
		default:
			counts.open++
		}
		if msg.Status.Armed() {
			counts.armed++
		}
	}
	parts := []string{stTitle.Render("herdr-outbox"),
		stDim.Render(fmt.Sprintf("%d 待处理", counts.open)),
		stDim.Render(fmt.Sprintf("%d 已就绪", counts.armed)),
		stDim.Render(fmt.Sprintf("%d 已发送", counts.sent))}
	parts = append(parts, stDim.Render("│"), stDim.Render("筛选: "+m.filter.Label()))
	if m.stepActive {
		parts = append(parts, stWarn.Render("轮询中…"))
	}
	if m.snapErr != nil {
		parts = append(parts, stAlert.Render("herdr 不可达"))
	}
	return fit(joinParts(parts), width)
}

func joinParts(parts []string) string { return strings.Join(parts, " ") }

func (m *Model) noticeView(width int) string {
	if m.notice == "" {
		return stDim.Render(strings.Repeat(" ", min(width, 1)))
	}
	age := time.Since(m.noticeAt)
	style := stDim
	if age < 6*time.Second {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("230"))
	}
	return fit(style.Render(truncate(m.notice, width-2)), width)
}

func (m *Model) listView(width, height int) string {
	head := stDim.Render(" 发件箱")
	lines := []string{head, strings.Repeat("─", max(width, 1))}
	list := m.visible()
	if len(list) == 0 {
		empty := "  还没有消息"
		hint := "  n  新建消息"
		if m.snapErr != nil {
			hint = "  r  重试 herdr"
		}
		lines = append(lines, "", stDim.Render(empty), stDim.Render(hint))
		return padBlock(lines, width, height)
	}
	window := listWindow(m.cursor, len(list), height-len(lines))
	for i := window.start; i < window.end; i++ {
		msg := list[i]
		rows := m.listRows(msg, width, i == m.cursor)
		lines = append(lines, rows...)
	}
	return padBlock(lines, width, height)
}

type window struct{ start, end int }

// listWindow keeps the cursor in view, showing a continuation marker when the list
// scrolls past either end.
func listWindow(cursor, n, height int) window {
	if height < 1 {
		height = 1
	}
	if n <= height {
		return window{0, n}
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start > n-height {
		start = n - height
	}
	return window{start, start + height}
}

func (m *Model) listRows(msg *model.Message, width int, selected bool) []string {
	glyph := statusGlyph(msg.Status)
	fav := " "
	if msg.Favorite {
		fav = "★"
	}
	title := msg.Title
	if title == "" {
		title = bodyPreview(msg.Content, 60)
	}
	if title == "" {
		title = "(新消息)"
	}
	title = truncateWidth(title, width-6)
	row := glyph + fav + " " + title
	if selected {
		row = stSelected.Render(pad(row, width-1))
	} else {
		row = styleByStatus(msg.Status).Render(pad(row, width-1))
	}
	// Both rows of an entry must come out exactly as wide as each other, or the
	// divider between the panels lands a column off on one of them.
	meta := pad("  "+truncateWidth(m.metaFor(msg), width-3), width-1)
	return []string{row, stDim.Render(meta)}
}

// metaFor is the quiet second line under each entry: where it goes and when.
func (m *Model) metaFor(msg *model.Message) string {
	switch msg.Status {
	case model.StatusSent:
		when := ""
		if msg.SentAt != nil {
			when = " " + msg.SentAt.Format("15:04")
		}
		return "已发送" + when + " → " + msg.Target.Display()
	case model.StatusScheduled:
		if msg.Trigger.SendAt != nil {
			return msg.Trigger.SendAt.Format("01-02 15:04") + " → " + msg.Target.Display()
		}
		return "定时 → " + msg.Target.Display()
	case model.StatusWaiting:
		// A waiting message can be blocked on something that will never happen, so
		// whatever the scheduler recorded is more useful than the bare status.
		if msg.LastError != "" {
			return "等待中: " + truncate(msg.LastError, 46)
		}
		pane, ok := m.paneFor(msg)
		if !ok {
			return "等待中: 目标已消失"
		}
		if pane.AgentStatus == herdr.StatusWorking {
			return "代理工作中，完成后发送 → " + pane.Display()
		}
		return "等待代理开始工作（" + herdr.StatusLabel(pane.AgentStatus) + "）→ " + pane.Display()
	case model.StatusFailed:
		return "失败: " + truncate(msg.LastError, 24)
	case model.StatusDraft:
		if msg.Target.Pane == "" {
			return "草稿 · 无目标"
		}
		return "草稿 → " + msg.Target.Display()
	default:
		return string(msg.Status) + " → " + msg.Target.Display()
	}
}

func statusGlyph(s model.Status) string {
	switch s {
	case model.StatusSent:
		return "✔"
	case model.StatusFailed:
		return "✘"
	case model.StatusSending:
		return "✦"
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

func styleByStatus(s model.Status) lipgloss.Style {
	switch s {
	case model.StatusSent:
		return stOk
	case model.StatusFailed:
		return stAlert
	case model.StatusSending:
		return lipgloss.NewStyle().Foreground(colWorking)
	case model.StatusWaiting, model.StatusScheduled:
		return lipgloss.NewStyle().Foreground(colAccent)
	case model.StatusPending:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	default:
		return stDim
	}
}

func (m *Model) detailPanel(width, height int) string {
	lines := []string{stDim.Render(" 消息"), strings.Repeat("─", max(width, 1))}
	sel := m.selected()
	if sel == nil {
		lines = append(lines, "", stDim.Render("  选择一条消息，或按 n 新建"))
		return padBlock(lines, width, height)
	}
	lines = append(lines, stDim.Render("  "+sel.ID))
	if preview := bodyPreview(sel.Content, width-4); preview != "" {
		lines = append(lines, stDim.Render("  "+preview))
	} else {
		lines = append(lines, stDim.Render("  (新消息)"))
	}
	lines = append(lines, "")

	content := sel.BodyLines()
	// Two lines are reserved for the metadata block below the body.
	avail := height - len(lines) - 4
	if avail < 1 {
		avail = 1
	}
	top := m.detailTop
	if top > len(content) {
		top = max(0, len(content)-avail)
		m.detailTop = top
	}
	shown := content[min(top, len(content)):]
	if len(shown) > avail {
		shown = shown[:avail]
	}
	for _, line := range shown {
		lines = append(lines, renderMarkdownLine(line, width))
	}
	if top > 0 {
		lines[len(lines)-1] = stDim.Render(fmt.Sprintf("  … 上方还有 %d 行 (ctrl-u)", top))
	}
	if top+len(shown) < len(content) {
		lines = append(lines, stDim.Render("  … 下方还有更多 (ctrl-d)"))
	}
	lines = append(lines, "")
	for _, meta := range m.metaBlock(sel, width) {
		lines = append(lines, meta)
	}
	return padBlock(lines, width, height)
}

func (m *Model) metaBlock(sel *model.Message, width int) []string {
	pane, live := m.paneFor(sel)
	target := "目标: " + sel.Target.Display()
	if live && sel.Target.PaneLabel != sel.Target.Pane {
		target += "  (" + pane.ID + ")"
	} else if !live && sel.Target.Pane != "" {
		target += "  (面板未找到)"
	}
	trigger := "触发: " + sel.Trigger.Kind.Label()
	switch sel.Trigger.Kind {
	case model.TriggerScheduled:
		if sel.Trigger.SendAt != nil {
			trigger += " @ " + sel.Trigger.SendAt.Format("2006-01-02 15:04")
		}
	case model.TriggerAfterCompletion:
		trigger += fmt.Sprintf(" (稳定 %d秒)", int(sel.SettleWindow()/time.Second))
	}
	out := []string{
		stDim.Render(truncateWidth(target, width)),
		stDim.Render(truncateWidth(trigger, width)),
		styleByStatus(sel.Status).Render("状态: " + sel.Status.Label()),
	}
	if sel.SentVia != "" {
		out = append(out, stDim.Render(truncateWidth("发送方式: "+sel.SentVia, width)))
	}
	if sel.LastError != "" {
		out = append(out, stAlert.Render(truncateWidth("错误: "+sel.LastError, width)))
	}
	return out
}

func renderMarkdownLine(line string, width int) string {
	trimmed := strings.TrimRight(line, " \t")
	switch {
	case strings.HasPrefix(trimmed, "#"):
		level := 0
		for _, r := range trimmed {
			if r != '#' {
				break
			}
			level++
		}
		text := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		prefix := strings.Repeat("  ", min(level, 3))
		if level >= 2 {
			return prefix + stHeading.Render(truncateWidth(text, width-len(prefix)))
		}
		return stHeading.Render(truncateWidth(text, width))
	case strings.HasPrefix(trimmed, "```"):
		return "  " + stDim.Render(truncateWidth(trimmed, width-2))
	case trimmed == "":
		return ""
	default:
		return "  " + truncateWidth(trimmed, width-2)
	}
}

func (m *Model) statusView(width int) string {
	parts := []string{}
	label := "herdr: " + m.provider.BackendName()
	focusedWS := ""
	if m.snapshot != nil {
		if m.poll > 0 && time.Since(m.snapshot.FetchedAt) > 3*time.Duration(m.poll) {
			label += " 已过期"
		}
		if m.snapshot.AgentSeqError != "" {
			label += " │ 无代理序号"
		}
		ws := ""
		for _, w := range m.snapshot.Workspaces {
			if w.Focused {
				ws = w.Display()
			}
		}
		focusedWS = ws
		if ws != "" {
			label += " │ " + ws
		}
	}
	parts = append(parts, stDim.Render(label))

	sel := m.selected()
	if sel != nil {
		if pane, ok := m.paneFor(sel); ok {
			agent := pane.Agent
			if agent == "" {
				agent = "shell"
			}
			status := pane.AgentStatus
			if status == "" {
				status = "unknown"
			}
			// The workspace is already on the bar, so name the pane again only when
			// the message points somewhere else.
			paneLabel := pane.Name()
			if focusedWS != "" && pane.WsLabel != focusedWS {
				paneLabel = pane.Display()
			}
			parts = append(parts,
				stDim.Render("面板:"), paneLabel,
				stDim.Render("代理:"), agent,
				statusStyle(status).Render(status),
			)
			if sid := pane.SessionID(); sid != "" {
				parts = append(parts, stDim.Render("会话:"+truncate(sid, 8)))
			}
		} else {
			parts = append(parts, stAlert.Render("目标面板已消失"))
		}
	}
	parts = append(parts, stDim.Render("·"), stDim.Render("? 帮助"))
	return fit(joinParts(parts), width)
}

func statusStyle(status string) lipgloss.Style {
	switch status {
	case herdr.StatusWorking:
		return lipgloss.NewStyle().Foreground(colWorking).Bold(true)
	case herdr.StatusBlocked:
		return lipgloss.NewStyle().Foreground(colBlock).Bold(true)
	case herdr.StatusIdle, herdr.StatusDone:
		return lipgloss.NewStyle().Foreground(colOk)
	default:
		return stDim
	}
}

// overlayView replaces the two panels rather than stacking on top of them, so the
// frame never grows past the terminal height and scrolls the status bar away.
func (m *Model) overlayView(width, bodyHeight int) string {
	switch m.mode {
	case modeEdit:
		return m.editOverlay(bodyHeight)
	case modeHelp:
		return padBlock(strings.Split(m.helpView(width), "\n"), width, bodyHeight)
	case modeLog:
		return padBlock(strings.Split(m.logOverlay(width, bodyHeight), "\n"), width, bodyHeight)
	case modeTargetPicker:
		if m.picker.step == 1 {
			return m.pickerSplitView(width, bodyHeight)
		}
		return padBlock(strings.Split(m.pickerOverlay(width, bodyHeight), "\n"), width, bodyHeight)
	case modeTriggerPicker:
		return padBlock(strings.Split(m.pickerOverlay(width, bodyHeight), "\n"), width, bodyHeight)
	case modeConfirmDelete:
		return padBlock(strings.Split(m.confirmOverlay(width), "\n"), width, bodyHeight)
	case modeRename:
		return padBlock(strings.Split(m.renameOverlay(width), "\n"), width, bodyHeight)
	}
	return ""
}

// pickerSplitView renders the target picker with a side-by-side layout:
// picker list on the left, pane preview on the right.
func (m *Model) pickerSplitView(width, bodyHeight int) string {
	leftW := width * 40 / 100
	if leftW < 30 {
		leftW = 30
	}
	if leftW > width-20 {
		leftW = width - 20
	}
	rightW := width - leftW - 1

	leftPanel := m.pickerLeftPanel(leftW, bodyHeight)
	rightPanel := m.pickerRightPanel(rightW, bodyHeight)

	divider := strings.TrimRight(strings.Repeat("│\n", bodyHeight), "\n")
	joined := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, divider, rightPanel)
	return joined
}

func (m *Model) pickerLeftPanel(width, bodyHeight int) string {
	p := &m.picker
	out := []string{stTitle.Render(p.title)}
	if p.filter != "" {
		out = append(out, "  筛选: "+p.filter)
	}
	avail := max(bodyHeight-4, 3)
	if len(p.items) == 0 {
		out = append(out, "", stDim.Render("  没有匹配 "+strconv.Quote(p.filter)))
		out = append(out, stDim.Render("  退格放宽 · esc 返回"))
		return padBlock(out, width, bodyHeight)
	}
	window := listWindow(p.cursor, len(p.items), avail)
	if window.start > 0 {
		out = append(out, stDim.Render("  ↑ "+strconv.Itoa(window.start)+" 更多"))
	}
	for i := window.start; i < window.end; i++ {
		item := p.items[i]
		label := "  " + truncateWidth(item.label, width-4)
		if i == p.cursor {
			out = append(out, stSelected.Render(pad(label, width-1)))
		} else {
			out = append(out, label)
		}
	}
	if window.end < len(p.items) {
		out = append(out, stDim.Render("  ↓ "+strconv.Itoa(len(p.items)-window.end)+" 更多"))
	}
	out = append(out, "", stDim.Render("输入筛选 · 回车选择 · esc 返回"))
	return padBlock(out, width, bodyHeight)
}

func (m *Model) pickerRightPanel(width, bodyHeight int) string {
	p := &m.picker
	out := []string{stDim.Render(" 面板预览")}
	out = append(out, strings.Repeat("─", max(width, 1)))

	if p.cursor >= len(p.items) {
		out = append(out, "", stDim.Render("  选择面板以预览内容"))
		return padBlock(out, width, bodyHeight)
	}

	item := p.items[p.cursor]
	out = append(out, stDim.Render("  "+item.pane.ID))
	if label := paneLabel(item.pane); label != "" {
		out = append(out, stTitle.Render("  "+truncateWidth(label, width-4)))
	}
	out = append(out, "")

	if p.panePreview == "" {
		out = append(out, stDim.Render("  加载中…"))
	} else {
		lines := strings.Split(p.panePreview, "\n")
		avail := bodyHeight - len(out) - 2
		if avail < 1 {
			avail = 1
		}
		if len(lines) > avail {
			lines = lines[:avail]
		}
		for _, line := range lines {
			out = append(out, "  "+truncateWidth(line, width-4))
		}
	}
	return padBlock(out, width, bodyHeight)
}

func (m *Model) helpView(width int) string {
	rows := [][2]string{
		{"n / e / Enter", "新建消息 · 编辑内容"},
		{"i", "重命名标题（留空显示内容预览）"},
		{"j k g G tab", "移动 · 跳转 · 切换列表和消息"},
		{"s", "立即发送"},
		{"t", "选择触发方式（手动、完成后、定时）"},
		{"p", "从 herdr 实时状态选择目标面板"},
		{"u", "暂存为草稿（不发送）"},
		{"f c d", "收藏 · 克隆 · 删除"},
		{"x", "切换筛选（全部、待处理、已发送）"},
		{"r R", "刷新 herdr 状态 · 执行一次调度"},
		{"l", "调度日志"},
		{"ctrl+d ctrl+u", "滚动长消息"},
		{"q  Q", "退出筛选 / 退出"},
	}
	out := make([]string, 0, len(rows)+3)
	out = append(out, stTitle.Render("快捷键"))
	for _, r := range rows {
		out = append(out, "  "+stOk.Render(pad(r[0], 15))+stDim.Render(r[1]))
	}
	if m.editorMode == config.EditorBuiltin {
		out = append(out, "  "+stDim.Render("内置编辑器: ctrl+s 保存 · 二次 esc 放弃；改配置见 herdr-outbox config editor"))
	} else {
		out = append(out, "  "+stDim.Render("外部编辑器: $EDITOR / $VISUAL；改配置见 herdr-outbox config editor"))
	}
	out = append(out, "  "+stDim.Render("数据以 Markdown 文件存储"))
	return lipgloss.NewStyle().Width(max(width, 1)).Render(strings.Join(out, "\n"))
}

func (m *Model) logOverlay(width, bodyHeight int) string {
	records, logPath, _ := m.provider.LogRecords(context.Background(), 12)
	out := []string{stTitle.Render("调度日志  " + logPath), ""}
	if len(m.log) > 0 {
		out = append(out, stDim.Render("本次会话"))
		for _, ev := range m.log {
			if len(out) > bodyHeight-5 {
				break
			}
			out = append(out, "  "+ev.At.Format("15:04:05")+" "+eventStyle(string(ev.Kind)).Render(string(ev.Kind))+
				" "+truncate(ev.Title, 24)+stDim.Render(" "+ev.Detail))
		}
	}
	if len(records) > 0 {
		out = append(out, "", stDim.Render("日志"))
		for _, r := range records {
			if len(out) > bodyHeight-2 {
				break
			}
			out = append(out, "  "+r.TS.Format("15:04:05")+" "+eventStyle(string(r.Phase)).Render(string(r.Phase))+
				" "+truncate(r.ID, 22)+stDim.Render(" "+r.Reason+" → "+r.Pane))
		}
	}
	out = append(out, "", stDim.Render("esc 关闭"))
	return strings.Join(out, "\n")
}

func eventStyle(k string) lipgloss.Style {
	switch k {
	case "sent", "acked", "recovered":
		return stOk
	case "failed", "skipped":
		return stAlert
	case "deferred", "attempted", "missed":
		return stWarn
	default:
		return stDim
	}
}

func (m *Model) pickerOverlay(width, bodyHeight int) string {
	p := m.picker
	out := []string{stTitle.Render(p.title)}
	if p.awaitingTime {
		out = append(out, "  "+p.prompt, "  > "+p.input+"▏", "", stDim.Render("回车确认 · esc 返回"))
		return strings.Join(out, "\n")
	}
	if p.filter != "" {
		out = append(out, "  筛选: "+p.filter)
	}
	avail := max(bodyHeight-4, 3)
	if len(p.items) == 0 {
		out = append(out, "", stDim.Render("  没有匹配 "+strconv.Quote(p.filter)))
		out = append(out, stDim.Render("  退格放宽 · esc 返回"))
		return strings.Join(out, "\n")
	}
	window := listWindow(p.cursor, len(p.items), avail)
	if window.start > 0 {
		out = append(out, stDim.Render("  ↑ "+strconv.Itoa(window.start)+" 更多"))
	}
	labelW := 0
	for i := window.start; i < window.end; i++ {
		// Two columns of indent plus one of gap, so the widest label still has a
		// space between it and its hint.
		if w := displayWidth(p.items[i].label) + 3; w > labelW {
			labelW = w
		}
	}
	labelW = min(labelW, max(width-12, 8))
	hintW := max(width-labelW, 8)
	for i := window.start; i < window.end; i++ {
		item := p.items[i]
		label := pad(truncateWidth("  "+item.label, labelW), labelW)
		// The highlight covers the label column only: banding the whole row would
		// leave no room for the status hint, which is what the choice turns on.
		if i == p.cursor {
			out = append(out, stSelected.Render(label)+stDim.Render(truncateWidth(item.hint, hintW)))
		} else {
			out = append(out, label+stDim.Render(truncateWidth(item.hint, hintW)))
		}
	}
	if window.end < len(p.items) {
		out = append(out, stDim.Render("  ↓ "+strconv.Itoa(len(p.items)-window.end)+" 更多"))
	}
	out = append(out, stDim.Render("输入筛选 · 回车选择 · esc 返回"))
	return strings.Join(out, "\n")
}

// editOverlay is the in-TUI editor: a title row, the text area, and a hint row.
// It replaces the two panels so the buffer gets the whole body area.
func (m *Model) editOverlay(bodyHeight int) string {
	title := stTitle.Render("编辑 ") + stDim.Render(truncate(m.editID, 20))
	if m.textarea.Value() != m.editOriginal {
		title += stWarn.Render(" ●")
	}
	hint := stDim.Render("ctrl+s 保存 · esc 放弃")
	if m.editConfirmDiscard {
		hint = stAlert.Render("有未保存的修改；再按一次 esc 放弃")
	}
	out := []string{title, m.textarea.View()}
	if spare := bodyHeight - 2 - lipgloss.Height(m.textarea.View()); spare > 0 {
		out = append(out, strings.Repeat("\n", spare))
	}
	out = append(out, hint)
	return strings.Join(out, "\n")
}

func (m *Model) confirmOverlay(width int) string {
	if m.deleteHold == nil {
		return ""
	}
	return stAlert.Render("永久删除 \"" + truncate(m.deleteHold.Summary(), 40) + "\"？(y 确认，其他键取消)")
}

func (m *Model) renameOverlay(width int) string {
	out := []string{stTitle.Render("重命名")}
	if m.renameInput == "" {
		out = append(out, "  留空则显示内容预览")
	}
	out = append(out, "  > "+m.renameInput+"▏")
	out = append(out, "", stDim.Render("回车确认 · esc 取消"))
	return strings.Join(out, "\n")
}

func padBlock(lines []string, width, height int) string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		lines[i] = fit(l, width)
	}
	return strings.Join(lines, "\n")
}

func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		// Truncate first: a line that reaches the pane's right edge would wrap and
		// push the rest of the layout down one row per overflow.
		lines[i] = pad(truncateWidth(strings.TrimRight(l, " "), width), width)
	}
	return strings.Join(lines, "\n")
}

func pad(s string, width int) string {
	w := displayWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// displayWidth and truncateWidth count what the terminal draws, not bytes, and
// skip ANSI sequences, so a styled row is measured and cut like a plain one.
// lipgloss is the same measurer JoinHorizontal uses to size the two panels, so a
// row can never come out wider than the column it was fitted into.
func displayWidth(s string) int { return lipgloss.Width(s) }

func truncateWidth(s string, width int) string {
	if width <= 1 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// bodyPreview returns the first meaningful line of Markdown body text,
// stripping heading markers and other formatting.
func bodyPreview(content string, maxRunes int) string {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		t = strings.TrimPrefix(t, ">")
		t = strings.TrimSpace(t)
		t = strings.Trim(t, "*_`")
		if t == "" {
			continue
		}
		r := []rune(t)
		if len(r) > maxRunes {
			return string(r[:maxRunes-1]) + "…"
		}
		return t
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
