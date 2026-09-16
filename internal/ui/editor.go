package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The editor is always the system $EDITOR. Implementing an in-TUI multi-line
// buffer would be worse for everyone who already has nvim configured.
func (m *Model) editCmd(id string) tea.Cmd {
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
	id     string
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
