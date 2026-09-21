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

	case "tab":
		m.filter.Status = filterMode((int(m.filter.Status) + 1) % 3)
		m.restoreCursor()
		return m, nil
	case "shift+tab":
		m.filter.Status = filterMode((int(m.filter.Status) + 2) % 3)
		m.restoreCursor()
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
		m.restoreCursor()
		return m, nil
	case "2":
		m.filter.Status = filterOpen
		m.restoreCursor()
		return m, nil
	case "3":
		m.filter.Status = filterSent
		m.restoreCursor()
		return m, nil

	case "x":
		m.openWorkspaceFilter()
		return m, nil

	case "r":
		m.setNotice("刷新 herdr 状态中")
		return m, tea.Batch(m.snapshotCmd(), m.stepCmdGuarded())

	case "R":
		return m, m.stepCmdGuarded()

	case "?":
		m.mode = modeHelp
		return m, nil
	case "l":
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
	case "c":
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
				m.setNotice("删除失败: " + err.Error())
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
		Trigger: model.Trigger{Kind: model.TriggerManual, SettleSeconds: 3},
		Status:  model.StatusDraft,
	}
	created, err := m.provider.CreateMessage(ctx, msg)
	if err != nil {
		m.setNotice("创建失败: " + err.Error())
		return m, nil
	}
	m.selectedID = created.ID
	m.setNotice("编辑中 " + created.ID)
	return m, tea.Batch(m.loadMsg(), m.editCmd(created.ID))
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
		m.setNotice("克隆失败: " + err.Error())
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
		m.setNotice("保存失败: " + err.Error())
	}
	return m, m.loadMsg()
}

// park stores a message without sending it, which is the "keep this for later"
// case in the brief.
func (m *Model) park(sel *model.Message) (tea.Model, tea.Cmd) {
	if sel.Status == model.StatusSending {
		m.setNotice("此消息正在发送中")
		return m, nil
	}
	draft := string(model.StatusDraft)
	if _, err := m.provider.UpdateMessage(context.Background(), sel.ID, api.UpdateRequest{Status: &draft}); err != nil {
		m.setNotice("保存失败: " + err.Error())
		return m, m.loadMsg()
	}
	m.setNotice("已暂存为草稿: " + sel.Summary())
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
	m.setNotice("正在发送 " + sel.Summary() + " 到 " + sel.Target.Display())
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
	sel := m.byID(id)
	if sel != nil && kind != model.TriggerManual && sel.Target.Pane == "" {
		m.picker = picker{}
		m.mode = modeBrowse
		m.setNotice("未设置目标面板；按 p 选择")
		return m, nil
	}
	m.picker = picker{}
	m.mode = modeBrowse
	ctx := context.Background()
	if _, err := m.provider.SetTrigger(ctx, id, kind, sendAt); err != nil {
		m.setNotice("触发设置失败: " + err.Error())
		return m, m.loadMsg()
	}
	m.setNotice("触发方式已设为: " + kind.Label())
	return m, tea.Batch(m.loadMsg(), m.snapshotCmd())
}

func (m *Model) afterEdit(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setNotice("编辑器异常退出: " + msg.err.Error())
	}
	before := m.byID(msg.id)
	verb := "已重新加载"
	if msg.saved {
		verb = "已保存"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if lp, ok := m.provider.(*LocalProvider); ok {
		updated, err := lp.Store.ReloadFromDisk(msg.id, orMessage(before))
		if err != nil {
			m.setNotice("重新加载失败: " + err.Error())
			return m, m.loadMsg()
		}
		if updated.Trigger.Kind != model.TriggerManual && updated.Status.Armed() {
			if _, err := lp.Engine.Arm(ctx, updated); err != nil && !herdr.IsUnavailable(err) {
				m.setNotice("重新设置失败: " + err.Error())
			}
			if err := lp.Store.Save(updated); err != nil {
				m.setNotice("保存失败: " + err.Error())
			}
		}
		m.setNotice(fmt.Sprintf("%s %s (%d 行)", verb, updated.ID, len(updated.BodyLines())))
	} else {
		updated, err := m.provider.GetMessage(ctx, msg.id)
		if err != nil {
			m.setNotice("重新加载失败: " + err.Error())
			return m, m.loadMsg()
		}
		m.setNotice(fmt.Sprintf("%s %s (%d 行)", verb, updated.ID, len(updated.BodyLines())))
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

func (m *Model) commitRename() (tea.Model, tea.Cmd) {
	id := m.renameID
	title := strings.TrimSpace(m.renameInput)
	m.mode = modeBrowse
	m.renameID = ""
	m.renameInput = ""
	ctx := context.Background()
	if _, err := m.provider.UpdateMessage(ctx, id, api.UpdateRequest{Title: &title}); err != nil {
		m.setNotice("重命名失败: " + err.Error())
		return m, m.loadMsg()
	}
	if title == "" {
		m.setNotice("已清除标题；将显示内容预览")
	} else {
		m.setNotice("已重命名为 " + title)
	}
	return m, m.loadMsg()
}
