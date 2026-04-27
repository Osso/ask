package main

import (
	"fmt"
	"net/url"
	"os"
)

// emitTermCwdFunc writes an OSC 7 sequence reporting `path` as the
// terminal's current working directory. Modern terminals (kitty,
// ghostty, wezterm, gnome-terminal, …) read this so a new tab/split
// opened from the same window inherits the path. Indirected through
// a package-level var so tests can capture the emitted path without
// touching /dev/tty.
var emitTermCwdFunc = writeOSC7ToTTY

// emitTermCwd is the package-internal entry point used by the rest of
// the app. Empty paths are silently ignored.
func emitTermCwd(path string) {
	if path == "" {
		return
	}
	emitTermCwdFunc(path)
}

func writeOSC7ToTTY(path string) {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	u := &url.URL{Scheme: "file", Host: host, Path: path}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer tty.Close()
	fmt.Fprintf(tty, "\x1b]7;%s\x1b\\", u.String())
}

// effectiveCwd returns the path that should be reported as ask's
// "current directory" for OSC 7 purposes: the active worktree dir
// when worktree mode has settled on one, otherwise m.cwd.
func (m *model) effectiveCwd() string {
	if m == nil {
		return ""
	}
	if m.worktreeName != "" && m.cwd != "" {
		return worktreePath(m.cwd, m.worktreeName)
	}
	return m.cwd
}

// currentEffectiveCwd returns the active tab's effective cwd, or "".
func (a app) currentEffectiveCwd() string {
	if a.active < 0 || a.active >= len(a.tabs) {
		return ""
	}
	return a.tabs[a.active].effectiveCwd()
}

// syncTermCwd emits OSC 7 for the active tab's effective cwd.
func (a app) syncTermCwd() {
	emitTermCwd(a.currentEffectiveCwd())
}
