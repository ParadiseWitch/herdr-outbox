package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"herdr-outbox/internal/config"
)

// newTextarea builds the in-TUI editor. The prompt marks each row so a wrapped
// body stays readable without a border that would eat two columns.
func newTextarea() textarea.Model {
	ta := textarea.New()
	ta.Prompt = "│ "
	ta.ShowLineNumbers = false
	ta.Placeholder = "在此输入消息内容"
	ta.CharLimit = 0
	ta.BlurredStyle.Base = lipgloss.NewStyle().Foreground(colDim)
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	return ta
}

// The editing surface is chosen by config: `builtin` keeps the user inside the
// TUI, `external` hands the terminal to $EDITOR for full editor power.
func (m *Model) editCmd(id string) tea.Cmd {
	if m.editorMode == config.EditorBuiltin {
		return m.openBuiltinEditor(id)
	}
	path, err := m.provider.FilePath(id)
	if err != nil {
		// Remote mode: download content to a temp file first.
		return m.downloadForEdit(id)
	}
	name, args := resolveEditor()
	cmd := execCommand(name, append(args, path)...)
	if cmd == nil {
		return func() tea.Msg {
			return noticeMsg{text: "未找到编辑器；请设置 $EDITOR（VISUAL 优先于 EDITOR）"}
		}
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorDoneMsg{id: id, err: err} })
}

// openBuiltinEditor loads the body through the provider so the in-TUI editor
// works the same whether the store is local or behind the server.
func (m *Model) openBuiltinEditor(id string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		content, err := m.provider.GetContent(ctx, id)
		if err != nil {
			return noticeMsg{text: "加载内容失败: " + err.Error()}
		}
		return builtinEditReadyMsg{id: id, content: content}
	}
}

// sizeTextarea fits the editor to the body area, leaving a title row above and a
// key-hint row below.
func (m *Model) sizeTextarea() {
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height - 4
	if h < 3 {
		h = 3
	}
	m.textarea.SetWidth(w - 4)
	m.textarea.SetHeight(h)
}

func (m *Model) onEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		return m.commitBuiltinEdit()
	case "ctrl+c":
		m.closeEditor()
		return m, nil
	case "esc":
		if m.textarea.Value() != m.editOriginal && !m.editConfirmDiscard {
			m.editConfirmDiscard = true
			return m, nil
		}
		m.closeEditor()
		m.setNotice("已放弃修改")
		return m, nil
	}
	m.editConfirmDiscard = false
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m *Model) closeEditor() {
	m.mode = modeBrowse
	m.editID = ""
	m.editOriginal = ""
	m.editConfirmDiscard = false
	m.textarea.Blur()
}

func (m *Model) commitBuiltinEdit() (tea.Model, tea.Cmd) {
	id, body, unchanged := m.editID, m.textarea.Value(), m.textarea.Value() == m.editOriginal
	m.closeEditor()
	if unchanged {
		return m, nil
	}
	return m, func() tea.Msg {
		ctx := context.Background()
		if _, err := m.provider.UpdateContent(ctx, id, body); err != nil {
			return noticeMsg{text: "保存失败: " + err.Error()}
		}
		// The rows and the preview render from the cached list, so the save has to
		// come back through the reload path rather than just reporting success.
		return editorDoneMsg{id: id, saved: true}
	}
}

// downloadForEdit fetches the message content and writes it to a temp file so
// the editor can work on it. The returned message triggers the editor step.
func (m *Model) downloadForEdit(id string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		content, err := m.provider.GetContent(ctx, id)
		if err != nil {
			return noticeMsg{text: "下载内容失败: " + err.Error()}
		}
		tmp, err := os.CreateTemp("", "herdr-outbox-edit-*.md")
		if err != nil {
			return noticeMsg{text: "创建临时文件失败: " + err.Error()}
		}
		tmpPath := tmp.Name()
		if _, err := tmp.WriteString(content); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return noticeMsg{text: "写入临时文件失败: " + err.Error()}
		}
		tmp.Close()
		return remoteEditReadyMsg{id: id, tmpPath: tmpPath}
	}
}

// remoteEditReadyMsg carries the temp file path for the editor step of a remote
// edit. The Update handler opens the editor via tea.ExecProcess.
type remoteEditReadyMsg struct {
	id      string
	tmpPath string
}

// uploadAfterEdit reads the edited temp file, uploads the content back to the
// server, and cleans up the temp file.
func (m *Model) uploadAfterEdit(id, tmpPath string) tea.Cmd {
	return func() tea.Msg {
		edited, err := os.ReadFile(tmpPath)
		os.Remove(tmpPath)
		if err != nil {
			return editorDoneMsg{id: id, err: err}
		}
		ctx := context.Background()
		if _, err := m.provider.UpdateContent(ctx, id, string(edited)); err != nil {
			return editorDoneMsg{id: id, err: err}
		}
		return editorDoneMsg{id: id}
	}
}

func (m *Model) openEditor(id string) (tea.Model, tea.Cmd) { return m, m.editCmd(id) }

// resolveEditor picks the editor command from the environment. VISUAL wins over
// EDITOR the way git and sudo do.
func resolveEditor() (string, []string) {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			return splitCommand(raw)
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad", nil
	}
	return "vi", nil
}

// splitCommand honours quoted editor settings such as `code --wait`, without
// running anything through a shell.
func splitCommand(raw string) (string, []string) {
	var (
		out     []string
		cur     strings.Builder
		quote   rune
		inToken bool
	)
	for _, r := range raw {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case r == ' ' || r == '\t':
			if inToken {
				out = append(out, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if cur.Len() > 0 || inToken {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return "", nil
	}
	return out[0], out[1:]
}

// execCommand builds the command, routing Windows batch shims through cmd.exe
// because CreateProcess cannot start a .cmd directly.
func execCommand(name string, args ...string) *exec.Cmd {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	resolved := name
	if found, err := exec.LookPath(name); err == nil {
		resolved = found
	} else if runtime.GOOS != "windows" {
		return nil
	}
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(resolved)) {
		case ".bat", ".cmd":
			full := append([]string{"/d", "/c", resolved}, args...)
			shell, err := exec.LookPath("cmd.exe")
			if err != nil {
				shell = "cmd.exe"
			}
			return exec.Command(shell, full...)
		}
	}
	return exec.Command(resolved, args...)
}
