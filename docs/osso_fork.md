# Osso fork: patches built on top of upstream

This document enumerates every behavioral change the Osso fork carries on top of
`upstream/main` (`Cidan/ask`). Use it as a checklist when rebasing onto a new
upstream: each section lists what the patch does, where it lives, how to
exercise it, and what to re-verify after the rebase lands.

Baseline: diff range `upstream/main..HEAD`. Regenerate the commit list with
`git log --oneline upstream/main..HEAD` when refreshing this doc.

Current patch stack:

```text
448a504 mcp: disable localhost bypass for DNS-rebinding protection
cff2248 shell: strip Anthropic credentials from shell subprocess env
4409dfd worktree: fix TOCTOU in ensureWorktreeGitignore via Lstat+atomic rename
27052f6 debug: use per-user XDG_STATE_HOME log path with O_NOFOLLOW
4e1ebf9 mcp: enforce 1 MiB argument size limit on tool handlers
e7c7d6b deploy: local build+test+install script
17c58c7 claude: route permission prompts to claude-bash-hook-approval MCP
9b6897f themes: add ayu (mirage) palette
b05db4a Render tool outputs in stream and session replay
```

---

## 1. MCP bridge hardening

**Purpose.** Keep the embedded MCP HTTP bridge reachable only through the
intended local channel, and reject oversized tool arguments before handlers try
to process them.

**Behavior details worth preserving.**
- `DisableLocalhostProtection` stays `false`, so the MCP SDK's localhost/Host
  protection remains active instead of bypassing DNS-rebinding checks.
- `ask_user_question` rejects raw argument payloads larger than 1 MiB before
  converting questions or touching UI state.
- The size limit is enforced against `req.Params.Arguments`, not the converted
  `askInput`, so malformed or adversarial payloads are capped too.

**Key files.**
- `mcp.go` (`maxMCPArgBytes`, `newMCPBridge`, `askTool`)

**Tests to re-run after rebase.**
- `go test ./...`
- Focused: `go test ./... -run 'TestMCP|TestConvertMCP|TestPermission'`

**Rebase risk.** Medium. Upstream is likely to touch the MCP bridge when the
question modal or approval tool changes. Watch for new MCP tools that accept
large argument bodies; apply the same raw-size check to each tool handler that
deserializes user/model-provided payloads.

---

## 2. Shell subprocess credential filtering

**Purpose.** Shell mode runs arbitrary user commands. It must not inherit
Claude/Anthropic credentials from the `ask` process environment.

**Behavior details worth preserving.**
- Shell commands use `cmd.Env = shellEnv()` instead of inheriting `os.Environ`
  implicitly.
- `shellEnv` strips `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`,
  `ANTHROPIC_BASE_URL`, `CLAUDE_APP_SECRET`, and `MCP_TIMEOUT`.
- The rest of the environment is preserved so normal shell mode behavior
  remains unchanged.

**Key files.**
- `shell.go` (`startShellCmd`, `shellEnv`)

**Tests to re-run after rebase.**
- `go test ./...`
- Add/keep a focused test if this area changes: set blocked env vars and assert
  `shellEnv()` omits only those keys.

**Rebase risk.** Low to medium. Upstream shell-mode work often touches
`startShellCmd`; make sure future refactors do not accidentally return to
implicit environment inheritance.

---

## 3. Worktree `.gitignore` TOCTOU hardening

**Purpose.** Worktree mode auto-appends `.claude/worktrees/` to the checkout's
top-level `.gitignore`. The fork avoids mutating a symlink and writes the update
atomically.

**Behavior details worth preserving.**
- `ensureWorktreeGitignore` uses `os.Lstat` and refuses to mutate `.gitignore`
  when it is a symlink.
- Existing file permissions are preserved; a new file defaults to `0644`.
- Writes go to a same-directory temp file followed by `os.Rename`, so readers
  never observe a partially written `.gitignore`.
- The function remains best-effort: errors are debug-logged and do not block app
  startup.

**Key files.**
- `worktree.go` (`ensureWorktreeGitignore`)
- `worktree_test.go`

**Tests to re-run after rebase.**
- `go test ./... -run Worktree`
- `go test ./...`

**Rebase risk.** Medium. Upstream worktree lifecycle changes can easily replace
the `.gitignore` helper. Preserve the Lstat/symlink refusal and atomic rename
even if surrounding worktree naming or pruning code changes.

---

## 4. Per-user debug log path

**Purpose.** Avoid the old shared `/tmp/ask.log` path and its symlink-attack
surface when `ASK_DEBUG=1` is enabled.

**Behavior details worth preserving.**
- Debug logs prefer `$XDG_STATE_HOME/ask/ask.log`, then
  `$HOME/.local/state/ask/ask.log`, then `$TMPDIR/ask-<uid>.log`.
- The log directory is created with `0700`.
- The log file is opened with `0600` and `O_NOFOLLOW`.
- Failure to create/open the log reports to stderr and disables debug logging
  rather than panicking.

**Key files.**
- `debug.go` (`debugLogPath`, `debugLog`)

**Tests to re-run after rebase.**
- `go test ./...`
- Add/keep focused tests if debug path behavior changes; be careful that
  `debugInit`/`debugOn` are package globals.

**Rebase risk.** Low. This file is small, but debug logging is used across async
boundaries, so preserve non-panicking behavior.

---

## 5. Claude permission approval MCP routing

**Purpose.** Route Claude Code permission prompts to the external
`claude-bash-hook-approval` MCP approval tool rather than the embedded `ask`
approval bridge.

**Behavior details worth preserving.**
- Claude CLI argv passes:
  `--permission-prompt-tool mcp__claude-bash-hook-approval__approval_prompt`.
- The existing ask approval UI/schema remains available for providers and tests,
  but Claude permission prompts are delegated to the external approval server.

**Key files.**
- `claude.go` (`claudeCLIArgs`)
- `claude_cli_test.go`

**Tests to re-run after rebase.**
- `go test ./... -run ClaudeCLIArgs`
- `go test ./...`

**Rebase risk.** Medium. Upstream provider-argument construction changes are
common. Re-check the exact tool name after any Claude MCP or hook integration
changes.

---

## 6. Local deploy helper

**Purpose.** Provide one local command that builds, tests, installs, and prints
the installed binary path plus commit.

**Behavior details worth preserving.**
- `deploy.sh` runs from the repository root regardless of caller cwd.
- It runs `go build ./...`, `go test ./...`, then `go install .`.
- It resolves the install path using `GOBIN` when set, otherwise
  `$(go env GOPATH)/bin`.

**Key files.**
- `deploy.sh`

**Tests to re-run after rebase.**
- `./deploy.sh`

**Rebase risk.** Low. Watch for upstream build/test command changes, new codegen
steps, or a module layout change that makes `go install .` insufficient.

---

## 7. Ayu Mirage theme

**Purpose.** Add an `ayu` theme matching Ayu Mirage's dark blue-grey palette
with warm amber accents.

**Behavior details worth preserving.**
- `ayuTheme()` is registered in `themeRegistry`.
- The theme sets explicit background and foreground colors, not only accent
  colors, so terminal OSC 10/11 matches the palette.

**Key files.**
- `themes.go` (`themeRegistry`, `ayuTheme`)

**Tests to re-run after rebase.**
- `go test ./...`
- Manual: open `/config` theme picker and select `ayu`.

**Rebase risk.** Low. Theme registry merge conflicts are straightforward, but
theme ordering matters for picker display.

---

## 8. Tool output rendering in live streams and replay

**Purpose.** Tool outputs should be visible in the transcript, not just tool
status lines or diffs. The fork renders stdout/stderr/error text during live
Claude/Codex streams and when replaying saved Claude sessions.

**Behavior details worth preserving.**
- Shared parsing handles both live `tool_use_result` and persisted
  `toolUseResult` wire keys.
- Output extraction accepts strings, nested arrays, stdout/stderr envelopes,
  generic `output`/`content`/`text`/`message`/`error` fields, and Anthropic
  `message.content` blocks with `type: "tool_result"`.
- `stderr`, explicit `is_error`, `error` fields, failed Codex statuses, and
  non-zero Codex exit codes mark output as error-styled.
- Live Claude `user` stream events emit both `toolDiffMsg` and `toolOutputMsg`
  when both are present.
- Live Codex `commandExecution` and `mcpToolCall` completion items emit
  `toolOutputMsg`.
- Claude session replay renders tool output as prerendered history, using the
  same output/error styles as the live stream.

**Key files.**
- `tool_output.go`
- `tool_output_test.go`
- `claude.go` (`readClaudeStream`)
- `codex.go` (`readCodexEvent`, `codexItemOutput`, `codexItemIsError`)
- `session.go` (`loadClaudeHistory`)
- `types.go` (`toolOutputMsg`)
- `update.go` (`toolOutputMsg` handling)
- `claude_stream_test.go`
- `codex_stream_test.go`
- `session_test.go`
- `update_test.go`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run 'ToolOutput|ToolResult|ClaudeStream|CodexStream|LoadClaudeHistory|Update'`

**Rebase risk.** High. This patch crosses both provider stream readers, session
replay, and the main update loop. Re-check every provider wire shape after an
upstream sync, especially if Codex app-server or Claude stream-json event names
change.

---

## Full verification

Before declaring a rebase complete:

```bash
go build ./...
go vet ./...
go test ./...
```

For release/install verification, run:

```bash
./deploy.sh
```
