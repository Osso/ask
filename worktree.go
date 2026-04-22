package main

import (
	"os"
	"path/filepath"
	"strings"
)

// ensureWorktreeGitignore makes sure the git checkout at cwd ignores
// `.claude/worktrees/`, the parent of every worktree `claude --worktree`
// spawns. No-op when cwd is not itself a git checkout (we don't walk
// upward — if the user launched ask in a subdir of a repo, that's their
// call) or when a rule already covers the path.
func ensureWorktreeGitignore() {
	if !inGitCheckout() {
		return
	}
	path := ".gitignore"
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		debugLog("worktree gitignore read %s: %v", path, err)
		return
	}
	if gitignoreCoversWorktrees(string(existing)) {
		return
	}
	next := string(existing)
	if len(next) > 0 && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	next += ".claude/worktrees/\n"
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		debugLog("worktree gitignore write %s: %v", path, err)
		return
	}
	debugLog("worktree gitignore added .claude/worktrees/ to %s", path)
}

// inGitCheckout returns true when cwd itself contains `.git` (directory
// in a normal checkout, regular file in a worktree / submodule).
func inGitCheckout() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(cwd, ".git"))
	return err == nil
}

func gitignoreCoversWorktrees(contents string) bool {
	for _, raw := range strings.Split(contents, "\n") {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		l = strings.TrimPrefix(l, "/")
		for changed := true; changed; {
			changed = false
			for _, suf := range []string{"/**", "/*", "/"} {
				if strings.HasSuffix(l, suf) {
					l = strings.TrimSuffix(l, suf)
					changed = true
				}
			}
		}
		if l == ".claude" || l == ".claude/worktrees" {
			return true
		}
	}
	return false
}
