package ui

import (
	"context"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
)

// pickerItem is one row of a flat list (the trigger picker). The herdr topology
// uses treeItem instead.
type pickerItem struct {
	key   string
	label string
	hint  string
}

type picker struct {
	title string
	items []pickerItem
	// cursor is the selected row in whichever list the picker shows.
	cursor int
	// target is the message id being changed.
	target       string
	awaitingTime bool
	input        string
	prompt       string

	// panePreview holds the preview content for the currently selected pane.
	panePreview     string
	panePreviewPane string // pane ID for which preview was loaded

	// treeItems holds the flattened tree; expanded holds the open/closed state of
	// each group row, keyed by row id.
	treeItems []treeItem
	expanded  map[string]bool
	// treeOpen is the state a group row gets when the user has not touched it:
	// the filter opens the whole tree, the target picker opens only the target's
	// own workspace and tab.
	treeOpen bool

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
	key   string
	label string
	hint  string
	// depth is the tree level: 0 workspace, 1 tab, 2 pane.
	depth     int
	isParent  bool
	expanded  bool
	workspace string
	// tab is the middle level's row key: a real tab id, or the synthetic id of
	// the "no tab" group. tabLabel is that level's display text, carried on a
	// pane row so the preview needs no lookup.
	tab      string
	tabLabel string
	pane     herdr.Pane
	target   model.Target
	// paneIDs are the panes below a group row, so one keystroke selects or
	// deselects a whole tab or workspace in the filter.
	paneIDs []string
}

// tabGroupID keys the middle tree level. Panes herdr reports without a tab id
// share one synthetic group per workspace so they stay reachable.
func tabGroupID(p herdr.Pane) string {
	if p.TabID == "" {
		return p.WorkspaceID + "|-"
	}
	return p.TabID
}

type tabGroup struct {
	id    string
	label string
	panes []herdr.Pane
}

// tabGroups groups a workspace's panes by tab, in the order they arrive, which
// Build already sorted by tab number then pane number.
func tabGroups(snap *herdr.Snapshot, panes []herdr.Pane) []tabGroup {
	groups := make([]tabGroup, 0, len(panes))
	at := make(map[string]int, len(panes))
	for _, p := range panes {
		id := tabGroupID(p)
		if i, ok := at[id]; ok {
			groups[i].panes = append(groups[i].panes, p)
			continue
		}
		label := id
		switch {
		case p.TabID == "":
			label = "未分栏"
		case snap != nil:
			if t, ok := snap.Tab(id); ok {
				label = t.Display()
			} else if name := herdr.TabName(id); name != "" {
				label = "标签 " + name
			}
		}
		at[id] = len(groups)
		groups = append(groups, tabGroup{id: id, label: label, panes: []herdr.Pane{p}})
	}
	return groups
}

// flattenTree renders the herdr topology as workspace → tab → pane. Group rows
// remember their state in m.picker.expanded; a group the user has not touched
// takes m.picker.treeOpen.
func (m *Model) flattenTree() []treeItem {
	if m.snapshot == nil {
		return nil
	}
	open := func(key string) bool {
		if state, ok := m.picker.expanded[key]; ok {
			return state
		}
		return m.picker.treeOpen
	}
	items := make([]treeItem, 0, len(m.snapshot.Workspaces)+len(m.snapshot.Tabs)+len(m.snapshot.Panes))
	for _, w := range m.snapshot.Workspaces {
		panes := make([]herdr.Pane, 0, 8)
		ids := make([]string, 0, 8)
		for _, p := range m.snapshot.Panes {
			if p.WorkspaceID == w.ID {
				panes = append(panes, p)
				ids = append(ids, p.ID)
			}
		}
		wsOpen := open(w.ID)
		items = append(items, treeItem{
			key:       w.ID,
			label:     w.Display(),
			hint:      pluralPanes(len(panes)),
			isParent:  true,
			expanded:  wsOpen,
			workspace: w.ID,
			paneIDs:   ids,
		})
		if !wsOpen {
			continue
		}
		for _, g := range tabGroups(m.snapshot, panes) {
			groupIDs := make([]string, 0, len(g.panes))
			for _, p := range g.panes {
				groupIDs = append(groupIDs, p.ID)
			}
			tabOpen := open(g.id)
			items = append(items, treeItem{
				key:       g.id,
				label:     g.label,
				hint:      pluralPanes(len(g.panes)),
				depth:     1,
				isParent:  true,
				expanded:  tabOpen,
				workspace: w.ID,
				tab:       g.id,
				paneIDs:   groupIDs,
			})
			if !tabOpen {
				continue
			}

			for _, p := range g.panes {
				items = append(items, treeItem{
					key:       p.ID,
					label:     p.Title(),
					hint:      paneHint(p),
					depth:     2,
					workspace: w.ID,
					tab:       g.id,
					tabLabel:  g.label,
					pane:      p,
					target:    targetOf(p),
				})
			}
		}
	}
	return items
}

// openAncestors records the workspace and tab that hold the given pane, so the
// tree opens on the message's current target instead of on nothing.
func (m *Model) openAncestors(paneID string) {
	if m.picker.expanded == nil {
		m.picker.expanded = map[string]bool{}
	}
	for _, p := range m.snapshot.Panes {
		if p.ID != paneID {
			continue
		}
		m.picker.expanded[p.WorkspaceID] = true
		m.picker.expanded[tabGroupID(p)] = true
		return
	}
}

func (m *Model) buildTreePicker() {
	m.picker.expanded = map[string]bool{}
	if m.snapshot == nil {
		m.picker.title = "目标面板"
		m.picker.cursor = 0
		return
	}
	sel := m.byID(m.picker.target)
	if sel != nil {
		if sel.Target.Pane != "" {
			m.openAncestors(sel.Target.Pane)
		} else {
			m.picker.expanded[sel.Target.Workspace] = true
		}
	}
	m.picker.treeItems = m.flattenTree()
	m.picker.title = "目标面板"

	if sel != nil && sel.Target.Pane != "" {
		for i, item := range m.picker.treeItems {
			if item.key == sel.Target.Pane {
				m.picker.cursor = i
				return
			}
		}
	}
	m.picker.cursor = 0
}

func (m *Model) rebuildTreeItems() {
	m.picker.treeItems = m.flattenTree()
}

// toggleTreeGroup opens or closes the group row under the cursor and leaves the
// cursor on the same row.
func (m *Model) toggleTreeGroup() {
	p := &m.picker
	if p.cursor >= len(p.treeItems) {
		return
	}
	item := p.treeItems[p.cursor]
	if !item.isParent {
		return
	}
	if p.expanded == nil {
		p.expanded = map[string]bool{}
	}
	p.expanded[item.key] = !item.expanded
	m.rebuildTreeItems()
	for i, it := range p.treeItems {
		if it.key == item.key {
			p.cursor = i
			return
		}
	}
	if p.cursor >= len(p.treeItems) {
		p.cursor = max(len(p.treeItems)-1, 0)
	}
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

func pluralPanes(n int) string {
	if n == 1 {
		return "1 个面板"
	}
	return strconv.Itoa(n) + " 个面板"
}

// paneHint is the quiet column on a pane row: what is running there, plus the
// stable name. The row label already carries the pane title, so repeating it
// here would say nothing; the ordinal is what survives a rename.
func paneHint(p herdr.Pane) string {
	status := p.AgentStatus
	if status == "" {
		status = herdr.StatusUnknown
	}
	if p.Agent == "" {
		status = "shell"
	}
	base := p.Ordinal
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
		m.toggleTreeGroup()
		return m, nil
	case "enter":
		if p.cursor >= len(p.treeItems) {
			return m, nil
		}
		item := p.treeItems[p.cursor]
		if item.isParent {
			// A group row is never a target: the key opens or closes it.
			m.toggleTreeGroup()
			return m, nil
		}
		return m.applyTarget(item.target)
	}
	return m, nil
}

type panePreviewMsg struct {
	paneID  string
	content string
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
	m.setNotice("目标已设为 " + m.targetLabel(t))
	return m, tea.Batch(m.loadMsg(), m.snapshotCmd())
}

// openWorkspaceFilter opens the workspace → tab → pane tree for multi-select filtering.
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
	// The filter panel is about narrowing, so every group starts open.
	m.picker.expanded = map[string]bool{}
	m.picker.treeOpen = true
	m.picker.treeItems = m.flattenTree()
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
		m.toggleTreeGroup()
		return m, nil
	case " ":
		if p.cursor >= len(p.treeItems) {
			return m, nil
		}
		item := p.treeItems[p.cursor]
		if p.selectedPanes == nil {
			p.selectedPanes = make(map[string]bool)
		}
		// A group row picks up its whole subtree, so one keystroke narrows the
		// list to a tab or to a workspace.
		ids := item.paneIDs
		if !item.isParent {
			ids = []string{item.key}
		}
		if len(ids) == 0 {
			return m, nil
		}
		selectAll := false
		for _, id := range ids {
			if !p.selectedPanes[id] {
				selectAll = true
				break
			}
		}
		for _, id := range ids {
			if selectAll {
				p.selectedPanes[id] = true
			} else {
				delete(p.selectedPanes, id)
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
