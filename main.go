package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type claudeResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

type slashCmd struct {
	name string
	desc string
}

var slashCmds = []slashCmd{
	{"/resume", "resume a previous Claude session"},
}

type sessionEntry struct {
	id      string
	preview string
	modTime time.Time
}

type viewMode int

const (
	modeInput viewMode = iota
	modeSessionPicker
)

type claudeDoneMsg struct {
	res claudeResult
	err error
	raw string
}

type sessionsLoadedMsg struct {
	sessions []sessionEntry
	err      error
}

var (
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	promptStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

type model struct {
	mode      viewMode
	input     textinput.Model
	spinner   spinner.Model
	renderer  *glamour.TermRenderer
	sessionID string
	busy      bool
	width     int

	menuIdx int

	sessions  []sessionEntry
	pickerIdx int
}

func initialModel() model {
	ti := textinput.New()
	ti.Placeholder = "ask anything (try /resume)"
	ti.Prompt = "> "
	ti.Focus()
	ti.CharLimit = 0

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)

	return model{
		mode:     modeInput,
		input:    ti,
		spinner:  sp,
		renderer: renderer,
		width:    100,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.Width = msg.Width - 4
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case claudeDoneMsg:
		m.busy = false
		var out string
		if msg.err != nil {
			out = errStyle.Render(fmt.Sprintf("error: %v", msg.err))
			if msg.raw != "" {
				out += "\n" + dimStyle.Render(msg.raw)
			}
		} else if msg.res.IsError {
			out = errStyle.Render("error: " + msg.res.Result)
		} else {
			if msg.res.SessionID != "" {
				m.sessionID = msg.res.SessionID
			}
			rendered, err := m.renderer.Render(msg.res.Result)
			if err != nil {
				out = msg.res.Result
			} else {
				out = strings.TrimRight(rendered, "\n")
			}
		}
		return m, tea.Println(out + "\n")

	case sessionsLoadedMsg:
		if msg.err != nil {
			return m, tea.Println(errStyle.Render(fmt.Sprintf("could not load sessions: %v", msg.err)))
		}
		if len(msg.sessions) == 0 {
			return m, tea.Println(dimStyle.Render("no prior sessions for this project"))
		}
		m.sessions = msg.sessions
		m.pickerIdx = 0
		m.mode = modeSessionPicker
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case modeSessionPicker:
			return m.updatePicker(msg)
		default:
			return m.updateInput(msg)
		}
	}
	return m, nil
}

func (m model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyCtrlD {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}

	items := m.filterSlashCmds()
	menuOpen := len(items) > 0

	switch msg.Type {
	case tea.KeyUp:
		if menuOpen {
			if m.menuIdx > 0 {
				m.menuIdx--
			}
			return m, nil
		}
	case tea.KeyDown:
		if menuOpen {
			if m.menuIdx < len(items)-1 {
				m.menuIdx++
			}
			return m, nil
		}
	case tea.KeyTab:
		if menuOpen {
			pick := items[m.menuIdx].name
			m.input.SetValue(pick)
			m.input.SetCursor(len(pick))
			return m, nil
		}
	case tea.KeyEnter:
		line := strings.TrimSpace(m.input.Value())
		if line == "" {
			return m, nil
		}
		m.input.SetValue("")
		m.menuIdx = 0
		if strings.HasPrefix(line, "/") {
			return m.handleCommand(line)
		}
		return m.sendToClaude(line)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if items := m.filterSlashCmds(); m.menuIdx >= len(items) {
		m.menuIdx = 0
	}
	return m, cmd
}

func (m model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.mode = modeInput
		return m, nil
	case tea.KeyUp:
		if m.pickerIdx > 0 {
			m.pickerIdx--
		}
	case tea.KeyDown:
		if m.pickerIdx < len(m.sessions)-1 {
			m.pickerIdx++
		}
	case tea.KeyEnter:
		if len(m.sessions) > 0 {
			m.sessionID = m.sessions[m.pickerIdx].id
			m.mode = modeInput
			return m, tea.Println(promptStyle.Render(
				fmt.Sprintf("✓ resumed session %s", short(m.sessionID))))
		}
	}
	return m, nil
}

func (m model) handleCommand(line string) (tea.Model, tea.Cmd) {
	cmd, _, _ := strings.Cut(line, " ")
	switch cmd {
	case "/resume":
		return m, loadSessionsCmd()
	default:
		return m, tea.Println(errStyle.Render("unknown command: " + cmd))
	}
}

func (m model) sendToClaude(line string) (tea.Model, tea.Cmd) {
	m.busy = true
	echo := promptStyle.Render("> ") + line
	return m, tea.Batch(
		tea.Println(echo),
		m.spinner.Tick,
		runClaudeCmd(line, m.sessionID),
	)
}

func (m model) View() string {
	if m.mode == modeSessionPicker {
		return m.viewPicker()
	}
	return m.viewInput()
}

func (m model) viewInput() string {
	var b strings.Builder
	if m.busy {
		b.WriteString(m.spinner.View())
		b.WriteString(dimStyle.Render("thinking…"))
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString(m.input.View())
	b.WriteString("\n")
	items := m.filterSlashCmds()
	if len(items) > 0 {
		b.WriteString("\n")
		for i, it := range items {
			line := fmt.Sprintf("  %s  %s", it.name, dimStyle.Render(it.desc))
			if i == m.menuIdx {
				line = selectedStyle.Render("▸ "+it.name) + "  " + dimStyle.Render(it.desc)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m model) viewPicker() string {
	var b strings.Builder
	b.WriteString(promptStyle.Render("select a session"))
	b.WriteString(dimStyle.Render("  (↑/↓ navigate · enter to resume · esc to cancel)"))
	b.WriteString("\n\n")
	for i, s := range m.sessions {
		preview := s.preview
		if preview == "" {
			preview = "(no preview)"
		}
		runes := []rune(preview)
		maxLen := m.width - 30
		if maxLen < 20 {
			maxLen = 20
		}
		if len(runes) > maxLen {
			preview = string(runes[:maxLen-1]) + "…"
		}
		row := fmt.Sprintf("  %s  %s  %s",
			dimStyle.Render(short(s.id)),
			dimStyle.Render(fmt.Sprintf("%6s ago", humanDuration(time.Since(s.modTime)))),
			preview,
		)
		if i == m.pickerIdx {
			row = selectedStyle.Render("▸ "+short(s.id)) +
				"  " + dimStyle.Render(fmt.Sprintf("%6s ago", humanDuration(time.Since(s.modTime)))) +
				"  " + selectedStyle.Render(preview)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return b.String()
}

func (m model) filterSlashCmds() []slashCmd {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") {
		return nil
	}
	var out []slashCmd
	for _, c := range slashCmds {
		if strings.HasPrefix(c.name, val) {
			out = append(out, c)
		}
	}
	return out
}

func runClaudeCmd(prompt, sessionID string) tea.Cmd {
	return func() tea.Msg {
		args := []string{"-p", prompt, "--output-format", "json", "--dangerously-skip-permissions"}
		if sessionID != "" {
			args = append(args, "--resume", sessionID)
		}
		c := exec.Command("claude", args...)
		var stdout, stderr bytes.Buffer
		c.Stdout = &stdout
		c.Stderr = &stderr
		if err := c.Run(); err != nil {
			return claudeDoneMsg{err: err, raw: stderr.String()}
		}
		var res claudeResult
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			return claudeDoneMsg{err: err, raw: stdout.String()}
		}
		return claudeDoneMsg{res: res}
	}
}

func loadSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		cwd, err := os.Getwd()
		if err != nil {
			return sessionsLoadedMsg{err: err}
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return sessionsLoadedMsg{err: err}
		}
		dir := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			return sessionsLoadedMsg{err: err}
		}
		var sessions []sessionEntry
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			full := filepath.Join(dir, e.Name())
			sessions = append(sessions, sessionEntry{
				id:      strings.TrimSuffix(e.Name(), ".jsonl"),
				preview: readSessionPreview(full),
				modTime: info.ModTime(),
			})
		}
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].modTime.After(sessions[j].modTime)
		})
		return sessionsLoadedMsg{sessions: sessions}
	}
}

func readSessionPreview(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		if t, _ := rec["type"].(string); t == "queue-operation" {
			if op, _ := rec["operation"].(string); op == "enqueue" {
				if c, _ := rec["content"].(string); c != "" {
					return strings.ReplaceAll(c, "\n", " ")
				}
			}
		}
		if msg, ok := rec["message"].(map[string]any); ok {
			if role, _ := msg["role"].(string); role == "user" {
				if s, ok := msg["content"].(string); ok && s != "" {
					return strings.ReplaceAll(s, "\n", " ")
				}
				if arr, ok := msg["content"].([]any); ok {
					for _, item := range arr {
						if im, ok := item.(map[string]any); ok {
							if txt, _ := im["text"].(string); txt != "" {
								return strings.ReplaceAll(txt, "\n", " ")
							}
						}
					}
				}
			}
		}
	}
	return ""
}

func short(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ask:", err)
		os.Exit(1)
	}
}
