package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
)

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

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(b)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func padRight(s string, w int) string {
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

func shortCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "?"
	}
	home, _ := os.UserHomeDir()
	p := cwd
	if home != "" && (cwd == home || strings.HasPrefix(cwd, home+string(os.PathSeparator))) {
		p = "~" + strings.TrimPrefix(cwd, home)
	}
	if p == "~" || p == string(os.PathSeparator) {
		return p
	}
	parts := strings.Split(p, string(os.PathSeparator))
	last := len(parts) - 1
	for i, part := range parts {
		if i == last || part == "" || part == "~" {
			continue
		}
		r := []rune(part)
		parts[i] = string(r[:1])
	}
	return strings.Join(parts, string(os.PathSeparator))
}
