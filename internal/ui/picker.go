package ui

import (
	"context"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
)

type pickerItem struct {
	key    string
	label  string
	hint   string
	pane   herdr.Pane
	target model.Target
}

type picker struct {
	title  string
	items  []pickerItem
	cursor int
	// step separates the workspace list from the pane list.
	step     int
	wsID     string
	itemsAll []pickerItem
	filter   string
	// target is the message id being changed.
	target string

	awaitingTime bool
	input        string
	prompt       string

	// panePreview holds the preview content for the currently selected pane.
	panePreview     string
	panePreviewPane string // pane ID for which preview was loaded
}

func (m *Model) openTargetPicker(ctx context.Context, sel *model.Message) {
	if m.snapshot == nil || len(m.snapshot.Panes) == 0 {
		m.setNotice("herdr 状态不可用；按 r 刷新")
		if m.snapErr != nil {
			m.setNotice("herdr 不可用: " + m.snapErr.Error())
		}
		return
	}
	m.mode = modeTargetPicker
	m.picker = picker{target: sel.ID, step: 0}
	m.buildWorkspaceStep()
}

func (m *Model) buildWorkspaceStep() {
	ws := m.snapshot.Workspaces
	items := make([]pickerItem, 0, len(ws))
	for _, w := range ws {
		count := 0
		for _, p := range m.snapshot.Panes {
			if p.WorkspaceID == w.ID {
				count++
			}
		}
		items = append(items, pickerItem{
			key:   w.ID,
			label: w.Display(),
			hint:  pluralPanes(count),
		})
	}
	if len(items) == 1 {
		m.buildPaneStep(items[0].key)
		return
	}
	m.picker.title = "目标工作区"
	m.picker.items = items
	m.picker.itemsAll = items
	m.picker.cursor = 0
	m.picker.step = 0
}

func pluralPanes(n int) string {
	if n == 1 {
		return "1 个面板"
	}
	return strconv.Itoa(n) + " 个面板"
}

func (m *Model) buildPaneStep(wsID string) {
	items := make([]pickerItem, 0, 8)
	for _, p := range m.snapshot.Panes {
		if p.WorkspaceID != wsID {
			continue
		}
		items = append(items, pickerItem{
			key:    p.ID,
			label:  paneLabel(p),
			hint:   paneHint(p),
			pane:   p,
			target: targetOf(p),
		})
	}
	m.picker.title = "目标面板 " + wsID
	m.picker.wsID = wsID
	m.picker.itemsAll = items
	m.picker.items = items
	m.picker.cursor = 0
	m.picker.step = 1
	m.picker.panePreview = ""
	m.picker.panePreviewPane = ""
}

// paneLabel builds a descriptive label for the picker, preferring terminal title
// over the ordinal name when available.
func paneLabel(p herdr.Pane) string {
	title := strings.TrimSpace(p.TerminalTitleClean)
	if title == "" {
		title = strings.TrimSpace(p.Label)
	}
	if title == "" {
		return p.Name()
	}
	// Truncate long titles to keep the picker readable.
	if len([]rune(title)) > 40 {
		title = string([]rune(title)[:37]) + "…"
	}
	return p.Name() + ": " + title
}

func paneHint(p herdr.Pane) string {
	status := p.AgentStatus
	if status == "" {
		status = herdr.StatusUnknown
	}
	if p.Agent == "" {
		status = "shell"
	}
	base := truncate(strings.TrimSpace(p.TerminalTitleClean), 34)
	if base == "" {
		base = p.ID
	}
	return status + " │ " + base
}

func (m *Model) onTargetPickerKey(key string) (tea.Model, tea.Cmd) {
	p := &m.picker
	switch key {
	case "esc":
		if p.step == 1 && len(m.snapshot.Workspaces) > 1 {
			m.buildWorkspaceStep()
			return m, nil
		}
		m.mode = modeBrowse
		return m, nil
	case "q":
		m.mode = modeBrowse
		return m, nil
	case "j", "down":
		if p.cursor < len(p.items)-1 {
			p.cursor++
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadPanePreview()
		}
		return m, nil
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadPanePreview()
		}
		return m, nil
	case "enter", "l", "right":
		if p.cursor >= len(p.items) {
			m.setNotice("没有匹配项；退格放宽筛选")
			return m, nil
		}
		item := p.items[p.cursor]
		if p.step == 0 {
			m.buildPaneStep(item.key)
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadPanePreview()
		}
		return m.applyTarget(item.target)
	}
	if len(key) == 1 && key[0] >= ' ' && key[0] <= '~' {
		p.filter += strings.ToLower(key)
		p.applyFilter()
		p.panePreview = ""
		p.panePreviewPane = ""
		return m, m.loadPanePreview()
	}
	if key == "backspace" {
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.applyFilter()
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadPanePreview()
		}
		return m, nil
	}
	return m, nil
}

// loadPanePreview asynchronously loads the preview for the currently selected pane.
func (m *Model) loadPanePreview() tea.Cmd {
	if m.picker.step != 1 || m.picker.cursor >= len(m.picker.items) {
		return nil
	}
	item := m.picker.items[m.picker.cursor]
	paneID := item.pane.ID
	// Don't reload if already loaded for this pane.
	if m.picker.panePreviewPane == paneID {
		return nil
	}
	return func() tea.Msg {
		content, err := m.provider.ReadPane(context.Background(), paneID, 20)
		if err != nil {
			return panePreviewMsg{paneID: paneID, content: "[error: " + err.Error() + "]"}
		}
		return panePreviewMsg{paneID: paneID, content: content}
	}
}

type panePreviewMsg struct {
	paneID  string
	content string
}

// applyFilter narrows the visible items without discarding the full list, so a
// mistyped character can be deleted again.
func (p *picker) applyFilter() {
	if p.filter == "" {
		p.items = p.itemsAll
		if p.cursor >= len(p.items) {
			p.cursor = len(p.items) - 1
		}
		return
	}
	var kept []pickerItem
	for _, it := range p.itemsAll {
		if strings.Contains(strings.ToLower(it.label+" "+it.hint+" "+it.key), p.filter) {
			kept = append(kept, it)
		}
	}
	if len(kept) == 0 {
		p.items = nil
		p.cursor = 0
		return
	}
	p.items = kept
	if p.cursor >= len(kept) {
		p.cursor = len(kept) - 1
	}
}

func (m *Model) applyTarget(t model.Target) (tea.Model, tea.Cmd) {
	id := m.picker.target
	m.picker = picker{}
	m.mode = modeBrowse
	ctx := context.Background()
	if _, err := m.provider.SetTarget(ctx, id, t); err != nil {
		m.setNotice("目标更改失败: " + err.Error())
		return m, m.loadMsg()
	}
	m.setNotice("目标已设为 " + t.Display())
	return m, tea.Batch(m.loadMsg(), m.snapshotCmd())
}
