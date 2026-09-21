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

	// treeItems holds the flattened tree for target selection.
	treeItems []treeItem

	// selectedPanes holds the multi-select choices for workspace filtering.
	selectedPanes map[string]bool
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
	m.picker = picker{target: sel.ID}
	m.buildTreePicker()
}

type treeItem struct {
	key       string
	label     string
	hint      string
	isParent  bool
	expanded  bool
	workspace string
	pane      herdr.Pane
	target    model.Target
}

func (m *Model) buildTreePicker() {
	sel := m.byID(m.picker.target)
	items := make([]treeItem, 0)

	if m.snapshot == nil {
		m.picker.treeItems = items
		m.picker.title = "目标面板"
		m.picker.cursor = 0
		return
	}

	for _, w := range m.snapshot.Workspaces {
		panes := make([]herdr.Pane, 0)
		for _, p := range m.snapshot.Panes {
			if p.WorkspaceID == w.ID {
				panes = append(panes, p)
			}
		}

		// Only expand the workspace that contains the message's current target.
		isExpanded := false
		if sel != nil && sel.Target.Workspace == w.ID {
			isExpanded = true
		}

		items = append(items, treeItem{
			key:      w.ID,
			label:    w.Display(),
			hint:     pluralPanes(len(panes)),
			isParent: true,
			expanded: isExpanded,
		})

		if isExpanded {
			for _, p := range panes {
				items = append(items, treeItem{
					key:       p.ID,
					label:     p.Title(),
					hint:      paneHint(p),
					isParent:  false,
					workspace: w.ID,
					pane:      p,
					target:    targetOf(p),
				})
			}
		}
	}

	m.picker.treeItems = items
	m.picker.title = "目标面板"

	if sel != nil && sel.Target.Pane != "" {
		for i, item := range items {
			if item.key == sel.Target.Pane {
				m.picker.cursor = i
				return
			}
		}
	}
	m.picker.cursor = 0
}

// rebuildTreeItems rebuilds the flat list from the current expand/collapse state.
func (m *Model) rebuildTreeItems() {
	// Preserve expand/collapse state from current treeItems.
	expanded := make(map[string]bool)
	for _, item := range m.picker.treeItems {
		if item.isParent {
			expanded[item.key] = item.expanded
		}
	}

	items := make([]treeItem, 0)
	for _, w := range m.snapshot.Workspaces {
		panes := make([]herdr.Pane, 0)
		for _, p := range m.snapshot.Panes {
			if p.WorkspaceID == w.ID {
				panes = append(panes, p)
			}
		}

		isExpanded := expanded[w.ID]

		items = append(items, treeItem{
			key:      w.ID,
			label:    w.Display(),
			hint:     pluralPanes(len(panes)),
			isParent: true,
			expanded: isExpanded,
		})

		if isExpanded {
			for _, p := range panes {
				items = append(items, treeItem{
					key:       p.ID,
					label:     p.Title(),
					hint:      paneHint(p),
					isParent:  false,
					workspace: w.ID,
					pane:      p,
					target:    targetOf(p),
				})
			}
		}
	}

	m.picker.treeItems = items
}

// loadTreePreview loads the preview for the currently selected pane in the tree.
func (m *Model) loadTreePreview() tea.Cmd {
	if m.picker.cursor >= len(m.picker.treeItems) {
		return nil
	}
	item := m.picker.treeItems[m.picker.cursor]
	if item.isParent || item.pane.ID == "" {
		return nil
	}
	paneID := item.pane.ID
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

func (m *Model) restorePickerCursor() {
	sel := m.byID(m.picker.target)
	if sel == nil {
		return
	}
	if m.picker.step == 0 {
		for i, item := range m.picker.items {
			if item.key == sel.Target.Workspace {
				m.picker.cursor = i
				return
			}
		}
	} else if m.picker.step == 1 {
		for i, item := range m.picker.items {
			if item.key == sel.Target.Pane {
				m.picker.cursor = i
				return
			}
		}
	}
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
	m.restorePickerCursor()
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
	case "esc", "q":
		m.mode = modeBrowse
		return m, nil
	case "j", "down":
		if p.cursor < len(p.treeItems)-1 {
			p.cursor++
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadTreePreview()
		}
		return m, nil
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadTreePreview()
		}
		return m, nil
	case "h", "left", "l", "right":
		if p.cursor >= len(p.treeItems) {
			return m, nil
		}
		item := p.treeItems[p.cursor]
		if item.isParent {
			// Toggle expand/collapse for this workspace.
			item.expanded = !item.expanded
			p.treeItems[p.cursor] = item
			m.rebuildTreeItems()
			// Keep cursor on the same workspace.
			for i, it := range p.treeItems {
				if it.key == item.key {
					p.cursor = i
					break
				}
			}
			return m, nil
		}
		return m, nil
	case "enter":
		if p.cursor >= len(p.treeItems) {
			return m, nil
		}
		item := p.treeItems[p.cursor]
		if item.isParent {
			// Toggle expand/collapse for this workspace.
			item.expanded = !item.expanded
			p.treeItems[p.cursor] = item
			m.rebuildTreeItems()
			for i, it := range p.treeItems {
				if it.key == item.key {
					p.cursor = i
					break
				}
			}
			return m, nil
		}
		// Leaf node: select this target.
		return m.applyTarget(item.target)
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

// openWorkspaceFilter opens a tree-based multi-select picker for workspace/pane filtering.
func (m *Model) openWorkspaceFilter() {
	if m.snapshot == nil || len(m.snapshot.Panes) == 0 {
		m.setNotice("herdr 状态不可用;按 r 刷新")
		return
	}
	m.mode = modeWorkspaceFilter
	m.picker = picker{}
	m.buildWorkspaceFilterTree()
}

func (m *Model) buildWorkspaceFilterTree() {
	items := make([]treeItem, 0)

	for _, w := range m.snapshot.Workspaces {
		panes := make([]herdr.Pane, 0)
		for _, p := range m.snapshot.Panes {
			if p.WorkspaceID == w.ID {
				panes = append(panes, p)
			}
		}

		items = append(items, treeItem{
			key:      w.ID,
			label:    w.Display(),
			hint:     pluralPanes(len(panes)),
			isParent: true,
			expanded: true,
		})

		for _, p := range panes {
			items = append(items, treeItem{
				key:       p.ID,
				label:     p.Title(),
				hint:      paneHint(p),
				isParent:  false,
				workspace: w.ID,
				pane:      p,
			})
		}
	}

	m.picker.treeItems = items
	m.picker.title = "筛选面板(空格选择,回车确认)"
	m.picker.cursor = 0
}

func (m *Model) onWorkspaceFilterKey(key string) (tea.Model, tea.Cmd) {
	p := &m.picker
	switch key {
	case "esc", "q":
		m.mode = modeBrowse
		return m, nil
	case "j", "down":
		if p.cursor < len(p.treeItems)-1 {
			p.cursor++
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadTreePreview()
		}
		return m, nil
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
			p.panePreview = ""
			p.panePreviewPane = ""
			return m, m.loadTreePreview()
		}
		return m, nil
	case "h", "left", "l", "right":
		if p.cursor >= len(p.treeItems) {
			return m, nil
		}
		item := p.treeItems[p.cursor]
		if item.isParent {
			item.expanded = !item.expanded
			p.treeItems[p.cursor] = item
			m.rebuildWorkspaceFilterTree()
			for i, it := range p.treeItems {
				if it.key == item.key {
					p.cursor = i
					break
				}
			}
		}
		return m, nil
	case " ":
		if p.cursor >= len(p.treeItems) {
			return m, nil
		}
		item := p.treeItems[p.cursor]
		if !item.isParent {
			// Toggle selection for this pane.
			if m.picker.selectedPanes == nil {
				m.picker.selectedPanes = make(map[string]bool)
			}
			if m.picker.selectedPanes[item.key] {
				delete(m.picker.selectedPanes, item.key)
			} else {
				m.picker.selectedPanes[item.key] = true
			}
		}
		return m, nil
	case "enter":
		// Apply the filter.
		if len(m.picker.selectedPanes) > 0 {
			m.filter.Panes = m.picker.selectedPanes
		} else {
			m.filter.Panes = nil
		}
		m.picker = picker{}
		m.mode = modeBrowse
		m.restoreCursor()
		m.setNotice("筛选: " + m.filter.Label())
		return m, nil
	}
	return m, nil
}

func (m *Model) rebuildWorkspaceFilterTree() {
	expanded := make(map[string]bool)
	for _, item := range m.picker.treeItems {
		if item.isParent {
			expanded[item.key] = item.expanded
		}
	}

	items := make([]treeItem, 0)
	for _, w := range m.snapshot.Workspaces {
		panes := make([]herdr.Pane, 0)
		for _, p := range m.snapshot.Panes {
			if p.WorkspaceID == w.ID {
				panes = append(panes, p)
			}
		}

		isExpanded := expanded[w.ID]

		items = append(items, treeItem{
			key:      w.ID,
			label:    w.Display(),
			hint:     pluralPanes(len(panes)),
			isParent: true,
			expanded: isExpanded,
		})

		if isExpanded {
			for _, p := range panes {
				items = append(items, treeItem{
					key:       p.ID,
					label:     p.Title(),
					hint:      paneHint(p),
					isParent:  false,
					workspace: w.ID,
					pane:      p,
				})
			}
		}
	}

	m.picker.treeItems = items
}
