package main

import (
	"encoding/json"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

var (
	approvalBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("11")).
				Padding(1, 2)
	approvalBtnStyle         = lipgloss.NewStyle().Padding(0, 3).Foreground(lipgloss.Color("8"))
	approvalDenyActiveStyle  = lipgloss.NewStyle().Padding(0, 3).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("9")).Bold(true)
	approvalAllowActiveStyle = lipgloss.NewStyle().Padding(0, 3).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("10")).Bold(true)
	approvalTitleStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	approvalToolStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	approvalKeyStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

const approvalBoxWidth = 90

func (m model) startApproval(msg approvalRequestMsg) model {
	m.mode = modeApproval
	m.approvalTool = msg.toolName
	m.approvalInput = msg.input
	m.approvalReply = msg.reply
	m.approvalChoice = 0
	return m
}

func (m model) clearApproval() model {
	m.mode = modeInput
	m.approvalTool = ""
	m.approvalInput = nil
	m.approvalReply = nil
	m.approvalChoice = 0
	return m
}

func (m model) sendApproval(allow bool) model {
	if m.approvalReply != nil {
		m.approvalReply <- approvalReply{allow: allow}
	}
	return m.clearApproval()
}

func (m model) updateApproval(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.Mod == tea.ModCtrl && msg.Code == 'd' {
		return m, tea.Quit
	}
	switch {
	case msg.Mod == tea.ModCtrl && msg.Code == 'c':
		m = m.sendApproval(false)
		m.killProc()
		m.appendHistory(outputStyle.Render(dimStyle.Render("✗ cancelled")))
		return m, nil
	case msg.Code == tea.KeyEsc, msg.Code == 'n' && msg.Mod == 0:
		return m.sendApproval(false), nil
	case msg.Code == 'y' && msg.Mod == 0:
		return m.sendApproval(true), nil
	case msg.Code == tea.KeyLeft, msg.Code == 'h' && msg.Mod == 0:
		m.approvalChoice = 0
		return m, nil
	case msg.Code == tea.KeyRight, msg.Code == 'l' && msg.Mod == 0:
		m.approvalChoice = 1
		return m, nil
	case msg.Code == tea.KeyTab:
		m.approvalChoice = 1 - m.approvalChoice
		return m, nil
	case msg.Code == tea.KeyEnter:
		return m.sendApproval(m.approvalChoice == 1), nil
	}
	return m, nil
}

func (m model) viewApproval() string {
	innerW := approvalBoxWidth - 6
	if innerW > m.width-6 {
		innerW = m.width - 6
	}
	if innerW < 24 {
		innerW = 24
	}

	title := approvalTitleStyle.Render("Approval required")
	tool := "Claude wants to use " + approvalToolStyle.Render(m.approvalTool)
	body := renderApprovalInput(m.approvalInput, innerW)

	deny := approvalBtnStyle.Render("Deny")
	allow := approvalBtnStyle.Render("Allow")
	if m.approvalChoice == 0 {
		deny = approvalDenyActiveStyle.Render("Deny")
	} else {
		allow = approvalAllowActiveStyle.Render("Allow")
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top, deny, "   ", allow)

	help := askHelpStyle.Render("←→ switch · enter confirm · y allow · n/esc deny · ctrl+c cancel turn")

	content := strings.Join([]string{title, "", tool, "", body, "", buttons, "", help}, "\n")
	return approvalBoxStyle.Width(innerW).Render(content)
}

func renderApprovalInput(input map[string]any, width int) string {
	if len(input) == 0 {
		return askSummaryDimStyle.Render("(no input)")
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("\n")
		}
		label := k + ":"
		val := formatApprovalValue(input[k], width-2)
		inline := !strings.Contains(val, "\n") && lipgloss.Width(label)+1+lipgloss.Width(val) <= width
		b.WriteString(approvalKeyStyle.Render(label))
		if inline {
			b.WriteString(" ")
			b.WriteString(val)
		} else {
			b.WriteString("\n  ")
			b.WriteString(strings.ReplaceAll(val, "\n", "\n  "))
		}
	}
	return b.String()
}

func formatApprovalValue(v any, width int) string {
	if width < 10 {
		width = 10
	}
	var s string
	switch val := v.(type) {
	case nil:
		return askSummaryDimStyle.Render("(null)")
	case string:
		s = val
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		s = string(b)
	}
	return truncateMultiLine(s, width, 12)
}

func truncateMultiLine(s string, width, maxLines int) string {
	if s == "" {
		return askSummaryDimStyle.Render(`""`)
	}
	lines := strings.Split(s, "\n")
	overflowed := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		overflowed = true
	}
	for i, ln := range lines {
		if lipgloss.Width(ln) > width {
			lines[i] = truncate(ln, width)
		}
	}
	if overflowed {
		lines = append(lines, askSummaryDimStyle.Render("…"))
	}
	return strings.Join(lines, "\n")
}
