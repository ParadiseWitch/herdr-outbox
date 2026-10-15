package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
)

const maxDetailScroll = 100000

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch m.mode {
	case modeEdit:
		return m.onEditKey(msg)
	case modeHelp:
		if key == "esc" || key == "?" || key == "q" || key == "ctrl+c" {
			m.mode = modeBrowse
		}
		return m, nil
	case modeLog:
		if key == "esc" || key == "l" || key == "q" {
			m.mode = modeBrowse
		}
		return m, nil
	case modeConfirmDelete:
		return m.onConfirmKey(key)
	case modeTargetPicker:
		return m.onTargetPickerKey(key)
	case modeTriggerPicker:
		return m.onTriggerPickerKey(key)
	case modeRename:
		return m.onRenameKey(msg)
	case modeWorkspaceFilter:
		return m.onWorkspaceFilterKey(key)
	case modeSearch:
		return m.onSearchKey(msg)
	}

	ctx := context.Background()
	sel := m.selected()

	switch key {
	case "ctrl+c", "Q":
		m.quitting = true
		return m, tea.Quit

	case "q":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.focus == focusDetail {
			m.focus = focusList
			return m, nil
		}
		if m.notice != "" {
			m.notice = ""
		}
		return m, nil

	case "j", "down":
		m.move(1)
		return m, nil
	case "k", "up":
		m.move(-1)
		return m, nil
	case "g", "home":
		m.cursor = 0
		m.restoreCursor()
		return m, nil
	case "G", "end":
		m.cursor = len(m.visible()) - 1
		m.restoreCursor()
		return m, nil

	case "tab", "l", "right":
		m.cycleTab(1)
		return m, nil
	case "shift+tab", "h", "left":
		m.cycleTab(-1)
		return m, nil
	case "ctrl+d":
		return m.scrollDetail(10)
	case "ctrl+u":
		return m.scrollDetail(-10)
	case "ctrl+f":
		return m.scrollDetail(m.detailHeight())
	case "ctrl+b":
		return m.scrollDetail(-m.detailHeight())

	case "1":
		m.filter.Status = filterAll
		m.persistTab()
		m.restoreCursor()
		return m, nil
	case "2":
		m.filter.Status = filterDraft
		m.persistTab()
		m.restoreCursor()
		return m, nil
	case "3":
		m.filter.Status = filterQueued
		m.persistTab()
		m.restoreCursor()
		return m, nil
	case "4":
		m.filter.Status = filterScheduled
		m.persistTab()
		m.restoreCursor()
		return m, nil
	case "5":
		m.filter.Status = filterSent
		m.persistTab()
		m.restoreCursor()
		return m, nil

	case "/":
		m.mode = modeSearch
		m.searchInput = m.filter.Query
		return m, nil

	case "x":
		m.openWorkspaceFilter()
		return m, nil

	case "c":
		m.clearScope()
		return m, nil

	case "r":
		m.setNotice("刷新 herdr 状态中")
		return m, tea.Batch(m.snapshotCmd(), m.stepCmdGuarded())

	case "R":
		return m, m.stepCmdGuarded()

	case "?":
		m.mode = modeHelp
		return m, nil
	case "L":
		m.mode = modeLog
		return m, nil

	case "n":
		return m.newMessage(ctx)
	case "e", "enter":
		if sel == nil {
			m.setNotice("未选择消息；按 n 新建")
			return m, nil
		}
		return m.openEditor(sel.ID)
	case "y":
		if sel != nil {
			return m.cloneMessage(sel)
		}
		return m, nil
	case "f":
		if sel != nil {
			return m.toggleFavorite(sel)
		}
		return m, nil
	case "d":
		if sel != nil {
			m.deleteHold = sel
			m.mode = modeConfirmDelete
		}
		return m, nil
	case "s":
		if sel != nil {
			return m.sendNow(sel)
		}
		m.setNotice("没有可发送的消息")
		return m, nil
	case "t":
		if sel != nil {
			m.openTriggerPicker(sel)
		}
		return m, nil
	case "p":
		if sel != nil {
			m.openTargetPicker(ctx, sel)
		}
		return m, nil
	case "u":
		if sel != nil {
			return m.park(sel)
		}
		return m, nil
	case "i":
		if sel != nil {
			m.mode = modeRename
			m.renameID = sel.ID
			m.renameInput = sel.Title
		}
		return m, nil
	}
	return m, nil
}

// cycleTab moves across the status tabs, wrapping in both directions, and
// remembers the choice for the next launch. The tab values are a dense 0..2
// range, so the modulo is over the count, not a hardcoded bound.
func (m *Model) cycleTab(delta int) {
	const count = int(filterSent) + 1
	n := (int(m.filter.Status) + delta) % count
	if n < 0 {
		n += count
	}
	m.filter.Status = filterMode(n)
	m.persistTab()
	m.restoreCursor()
}

func (m *Model) stepCmdGuarded() tea.Cmd {
	if m.stepActive {
		m.deferTick = true
		return nil
	}
	m.stepActive = true
	return m.stepCmd()
}

func (m *Model) scrollDetail(delta int) (tea.Model, tea.Cmd) {
	m.detailTop += delta
	if m.detailTop < 0 {
		m.detailTop = 0
	}
	if m.detailTop > maxDetailScroll {
		m.detailTop = maxDetailScroll
	}
	return m, nil
}

func (m *Model) onConfirmKey(key string) (tea.Model, tea.Cmd) {
	sel := m.deleteHold
	switch key {
	case "y", "Y", "enter":
		if sel != nil {
			if err := m.provider.DeleteMessage(context.Background(), sel.ID); err != nil {
				m.setNotice("删除失败：" + err.Error())
			} else {
				m.setNotice("已删除 " + sel.Summary())
				m.selectedID = ""
				if m.cursor > 0 {
					m.cursor--
				}
			}
		}
		m.deleteHold = nil
		m.mode = modeBrowse
		return m, m.loadMsg()
	default:
		m.deleteHold = nil
		m.mode = modeBrowse
		return m, nil
	}
}

func (m *Model) newMessage(ctx context.Context) (tea.Model, tea.Cmd) {
	msg := &model.Message{
		Target:  m.defaultTarget(),
		Trigger: model.Trigger{Kind: model.TriggerManual, SettleSeconds: 3},
		Status:  model.StatusDraft,
	}
	created, err := m.provider.CreateMessage(ctx, msg)
	if err != nil {
		m.setNotice("创建失败：" + err.Error())
		return m, nil
	}
	m.selectedID = created.ID
	// Creating from a lifecycle tab pre-selects that tab's trigger, so a message
	// you start while looking at 队列中 or 定时中 lands back there instead of in
	// 草稿. Deferred to afterEdit because a scheduled trigger needs a send_at and
	// an after_completion one needs content + target, none of which a fresh draft
	// has yet. 全部/草稿/已发送 keep the manual default.
	m.pendingTrigger, m.pendingTriggerID = triggerForTab(m.filter.Status), created.ID
	note := "编辑中 " + created.ID
	switch m.pendingTrigger {
	case model.TriggerAfterCompletion:
		note += "（保存后设为完成后发送）"
	case model.TriggerScheduled:
		note += "（保存后设置发送时间）"
	}
	if !created.Target.Empty() {
		note += " · 目标 " + m.targetDisplay(created) + "（按 p 可改）"
	}
	m.setNotice(note)
	return m, tea.Batch(m.loadMsg(), m.editCmd(created.ID))
}

// triggerForTab is the trigger a new message adopts when created from a lifecycle
// tab, so the tab it lands in matches where you started it. 队列中 means "waiting
// on the agent", hence after_completion; 定时中 is the clock. The other tabs have
// no single trigger to imply, so they keep the manual default.
func triggerForTab(tab filterMode) model.TriggerKind {
	switch tab {
	case filterQueued:
		return model.TriggerAfterCompletion
	case filterScheduled:
		return model.TriggerScheduled
	default:
		return model.TriggerManual
	}
}

func targetOf(p herdr.Pane) model.Target { return p.Target() }

func (m *Model) cloneMessage(src *model.Message) (tea.Model, tea.Cmd) {
	dup := *src
	dup.ID = ""
	dup.Status = model.StatusDraft
	dup.SentAt = nil
	dup.Attempts = 0
	dup.LastError = ""
	dup.ClearBaseline()
	dup.SentVia = ""
	dup.Title = src.Title
	if dup.Title == "" {
		dup.Title = model.DeriveTitle(src.Content)
	}
	created, err := m.provider.CreateMessage(context.Background(), &dup)
	if err != nil {
		m.setNotice("克隆失败：" + err.Error())
		return m, nil
	}
	m.selectedID = created.ID
	m.setNotice("已克隆为 " + created.ID)
	return m, m.loadMsg()
}

func (m *Model) toggleFavorite(sel *model.Message) (tea.Model, tea.Cmd) {
	sel.Favorite = !sel.Favorite
	fav := sel.Favorite
	if _, err := m.provider.UpdateMessage(context.Background(), sel.ID, api.UpdateRequest{Favorite: &fav}); err != nil {
		m.setNotice("保存失败：" + err.Error())
	}
	return m, m.loadMsg()
}

// park stores a message without sending it, which is the "keep this for later"
// case in the brief.
func (m *Model) park(sel *model.Message) (tea.Model, tea.Cmd) {
	if sel.Status == model.StatusSending {
		m.setNotice("消息正在发送中")
		return m, nil
	}
	draft := string(model.StatusDraft)
	if _, err := m.provider.UpdateMessage(context.Background(), sel.ID, api.UpdateRequest{Status: &draft}); err != nil {
		m.setNotice("保存失败：" + err.Error())
		return m, m.loadMsg()
	}
	m.setNotice("已存为草稿：" + sel.Summary())
	return m, m.loadMsg()
}

func (m *Model) sendNow(sel *model.Message) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(sel.Content) == "" {
		m.setNotice("消息内容为空")
		return m, nil
	}
	if sel.Target.Pane == "" {
		m.setNotice("未设置目标面板；按 p 选择")
		return m, nil
	}
	if m.stepActive {
		m.deferTick = true
	}
	id := sel.ID
	m.setNotice("发送中 " + sel.Summary() + " → " + m.targetDisplay(sel))
	return m, func() tea.Msg {
		evs, err := m.provider.SendNow(context.Background(), id)
		return sendDoneMsg{id: id, events: evs, err: err}
	}
}

type sendDoneMsg struct {
	id     string
	events []scheduler.Event
	err    error
}

func (m *Model) openTriggerPicker(sel *model.Message) {
	m.mode = modeTriggerPicker
	m.picker = picker{
		title: "触发方式: " + truncate(sel.Summary(), 28),
		items: []pickerItem{
			{key: "manual", label: "手动", hint: "按 s 时发送"},
			{key: "after_completion", label: "完成后", hint: "等待代理空闲后发送"},
			{key: "scheduled", label: "定时", hint: "在指定时间发送"},
		},
		cursor: triggerIndex(sel.Trigger.Kind),
		target: sel.ID,
	}
}

func triggerIndex(k model.TriggerKind) int {
	switch k {
	case model.TriggerAfterCompletion:
		return 1
	case model.TriggerScheduled:
		return 2
	default:
		return 0
	}
}

func (m *Model) onTriggerPickerKey(key string) (tea.Model, tea.Cmd) {
	p := &m.picker
	switch key {
	case "esc", "q":
		m.mode = modeBrowse
		return m, nil
	case "j", "down":
		if p.cursor < len(p.items)-1 {
			p.cursor++
		}
		return m, nil
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case "enter":
		if p.awaitingTime {
			return m.confirmSchedule(key)
		}
		item := p.items[p.cursor]
		switch item.key {
		case "manual", "after_completion":
			return m.applyTrigger(model.TriggerKind(item.key), nil)
		case "scheduled":
			p.awaitingTime = true
			p.input = ""
			p.prompt = "发送时间 (20m, 09:30, 2026-09-17 02:00)"
			return m, nil
		}
		return m, nil
	}
	if p.awaitingTime {
		return m.confirmSchedule(key)
	}
	return m, nil
}

func (m *Model) confirmSchedule(key string) (tea.Model, tea.Cmd) {
	p := &m.picker
	switch key {
	case "backspace":
		if len(p.input) > 0 {
			p.input = p.input[:len(p.input)-1]
		}
		return m, nil
	case "enter", "ctrl+s":
		raw := strings.TrimSpace(p.input)
		at, err := model.ParseSendAt(model.Now(), raw)
		if err != nil {
			m.setNotice(err.Error())
			return m, nil
		}
		return m.applyTrigger(model.TriggerScheduled, at)
	case "esc":
		// Reached from the trigger list: go back to it. Reached directly (a new
		// message from the 定时中 tab): there is no list behind us, so leave the
		// picker entirely rather than strand the user on an empty one.
		if len(p.items) == 0 {
			m.picker = picker{}
			m.mode = modeBrowse
			return m, m.loadMsg()
		}
		p.awaitingTime = false
		p.input = ""
		p.prompt = ""
		return m, nil
	default:
		if len(key) == 1 && (key[0] >= ' ' && key[0] <= '~') {
			p.input += key
		}
		return m, nil
	}
}

func (m *Model) applyTrigger(kind model.TriggerKind, sendAt *time.Time) (tea.Model, tea.Cmd) {
	id := m.picker.target
	m.picker = picker{}
	m.mode = modeBrowse
	return m.setTriggerFor(id, kind, sendAt)
}

// setTriggerFor arms one message with a trigger, by id. applyTrigger wraps it
// with the picker teardown; afterEdit calls it directly for a message whose
// trigger was deferred until it had content.
func (m *Model) setTriggerFor(id string, kind model.TriggerKind, sendAt *time.Time) (tea.Model, tea.Cmd) {
	if sel := m.byID(id); sel != nil && kind != model.TriggerManual && sel.Target.Pane == "" {
		m.setNotice("未设置目标面板；按 p 选择")
		return m, nil
	}
	ctx := context.Background()
	if _, err := m.provider.SetTrigger(ctx, id, kind, sendAt); err != nil {
		m.setNotice("触发设置失败：" + err.Error())
		return m, m.loadMsg()
	}
	m.setNotice("触发已设为：" + kind.Label())
	return m, tea.Batch(m.loadMsg(), m.snapshotCmd())
}

// openSchedulePrompt drops the user straight into the send-time input for a
// message, skipping the trigger list -- used when a new message was created from
// the 定时中 tab, where the trigger is already decided and only the time is left.
func (m *Model) openSchedulePrompt(id string) (tea.Model, tea.Cmd) {
	title := "定时发送"
	if sel := m.byID(id); sel != nil {
		title = "定时发送: " + truncate(sel.Summary(), 28)
	}
	m.mode = modeTriggerPicker
	m.picker = picker{
		title:        title,
		target:       id,
		awaitingTime: true,
		prompt:       "发送时间 (20m, 09:30, 2026-09-17 02:00)",
	}
	return m, nil
}

func (m *Model) afterEdit(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setNotice("编辑器异常退出：" + msg.err.Error())
	}
	before := m.byID(msg.id)
	verb := "已重新加载"
	if msg.saved {
		verb = "已保存"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// A new message created from the 队列中/定时中 tab carries a trigger to adopt
	// now that it has content. Taken here and cleared unconditionally so a
	// discarded edit cannot leave it armed for the next message.
	deferTrigger, deferID := m.pendingTrigger, m.pendingTriggerID
	m.pendingTrigger, m.pendingTriggerID = "", ""

	if lp, ok := m.provider.(*LocalProvider); ok {
		updated, err := lp.Store.ReloadFromDisk(msg.id, orMessage(before))
		if err != nil {
			m.setNotice("重新加载失败：" + err.Error())
			return m, m.loadMsg()
		}
		if updated.Trigger.Kind != model.TriggerManual && updated.Status.Armed() {
			if _, err := lp.Engine.Arm(ctx, updated); err != nil && !herdr.IsUnavailable(err) {
				m.setNotice("重新设置失败：" + err.Error())
			}
			if err := lp.Store.Save(updated); err != nil {
				m.setNotice("保存失败：" + err.Error())
			}
		}
		m.setNotice(fmt.Sprintf("%s %s (%d 行)", verb, updated.ID, len(updated.BodyLines())))
	} else {
		updated, err := m.provider.GetMessage(ctx, msg.id)
		if err != nil {
			m.setNotice("重新加载失败：" + err.Error())
			return m, m.loadMsg()
		}
		m.setNotice(fmt.Sprintf("%s %s (%d 行)", verb, updated.ID, len(updated.BodyLines())))
	}
	// Adopt the tab's trigger now that the message exists with content. An empty
	// draft (editor discarded) stays manual -- arming a completion or a timer on
	// nothing would schedule a send with no body to deliver.
	if deferID == msg.id {
		if cur, cerr := m.provider.GetMessage(ctx, msg.id); cerr == nil && strings.TrimSpace(cur.Content) != "" {
			switch deferTrigger {
			case model.TriggerAfterCompletion:
				return m.setTriggerFor(msg.id, model.TriggerAfterCompletion, nil)
			case model.TriggerScheduled:
				return m.openSchedulePrompt(msg.id)
			}
		}
	}
	return m, tea.Batch(m.loadMsg(), m.snapshotCmd())
}

func orMessage(m *model.Message) *model.Message {
	if m != nil {
		return m
	}
	return &model.Message{}
}

func truncate(s string, n int) string { return truncateWidth(s, n) }

func (m *Model) onRenameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBrowse
		m.renameID = ""
		m.renameInput = ""
		return m, nil
	case "enter":
		return m.commitRename()
	case "backspace":
		if len(m.renameInput) > 0 {
			// Remove the last rune, not the last byte, so multi-byte characters
			// (e.g. Chinese) are deleted correctly.
			runes := []rune(m.renameInput)
			m.renameInput = string(runes[:len(runes)-1])
		}
		return m, nil
	}
	// Runes holds printable input only, so named keys like "ctrl+w" or "up"
	// never leak into the title the way a String() comparison would allow.
	if len(msg.Runes) > 0 {
		m.renameInput += string(msg.Runes)
	}
	return m, nil
}

func (m *Model) onSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBrowse
		m.filter.Query = ""
		m.searchInput = ""
		m.restoreCursor()
		return m, nil
	case "enter":
		m.mode = modeBrowse
		return m, nil
	case "backspace":
		if len(m.searchInput) > 0 {
			runes := []rune(m.searchInput)
			m.searchInput = string(runes[:len(runes)-1])
		}
		m.filter.Query = m.searchInput
		m.restoreCursor()
		return m, nil
	}
	if len(msg.Runes) > 0 {
		m.searchInput += string(msg.Runes)
		m.filter.Query = m.searchInput
		m.restoreCursor()
	}
	return m, nil
}

func (m *Model) commitRename() (tea.Model, tea.Cmd) {
	id := m.renameID
	title := strings.TrimSpace(m.renameInput)
	m.mode = modeBrowse
	m.renameID = ""
	m.renameInput = ""
	ctx := context.Background()
	if _, err := m.provider.UpdateMessage(ctx, id, api.UpdateRequest{Title: &title}); err != nil {
		m.setNotice("重命名失败：" + err.Error())
		return m, m.loadMsg()
	}
	if title == "" {
		m.setNotice("已清除标题；将显示内容预览")
	} else {
		m.setNotice("已重命名为 " + title)
	}
	return m, m.loadMsg()
}
