# Osso fork: patches built on top of upstream

This document enumerates every behavioral change the Osso fork carries on top of
`upstream/main` (`Cidan/ask`). Use it as a checklist when rebasing onto a new
upstream: each section lists what the patch does, where it lives, how to
exercise it, and what to re-verify after the rebase lands.

Baseline: diff range `upstream/main..HEAD`. Regenerate the patch list with
`git log --oneline upstream/main..HEAD`, then omit documentation-only commits
and patches that have intentionally been removed from the stack.

Current patch stack:

```text
11c4d0e Normalize Bubble Tea key matching
fa86de9 Make Ctrl-D quit ask
3ffee0a Add provider switch command
ed01956 Render tool outputs in stream and session replay
322b43b themes: add ayu (mirage) palette
8f114e3 claude: route permission prompts to claude-bash-hook-approval MCP
cfd2dad deploy: local build+test+install script
b187d8c mcp: enforce 1 MiB argument size limit on tool handlers
1dbebdb worktree: fix TOCTOU in ensureWorktreeGitignore via Lstat+atomic rename
60751bd shell: strip Anthropic credentials from shell subprocess env
e57465c mcp: disable localhost bypass for DNS-rebinding protection
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

## 4. Claude permission approval MCP routing

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

## 5. Local deploy helper

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

## 6. Ayu Mirage theme

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

## 7. Tool output rendering in live streams and replay

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

## 8. Ctrl+D quits the whole app

**Purpose.** Match Codex CLI's `Ctrl+D` behavior: quit the app immediately
instead of closing only the active tab.

**Behavior details worth preserving.**
- The app wrapper intercepts `Ctrl+D` before dispatching to the active tab.
- `Ctrl+D` matching accepts both Bubble Tea v2 forms: `ModCtrl + 'd'`,
  `msg.String()`/`msg.Keystroke()` equal to `ctrl+d`, and the raw control-code
  shape (`0x04`).
- Quit drains pending replies, kills provider and shell subprocesses, and stops
  each tab's MCP bridge before returning `tea.Quit`.
- `Ctrl+C` twice on an empty idle prompt remains the tab-close path.

**Key files.**
- `util.go` (`isCtrlKey`)
- `tabs.go` (`app.Update`, `app.quit`)
- `tabs_test.go`
- `README.md`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run TestApp_CtrlD`

**Rebase risk.** Low to medium. This lives in the app-level key dispatcher;
re-check if upstream changes tab lifecycle, quit handling, or cleanup paths.

---

## 9. Provider switch command and Codex model forwarding

**Purpose.** Make provider switching reachable without relying on a terminal
delivering `Ctrl+B`, and ensure the selected Codex model is actually sent to
Codex app-server.

**Behavior details worth preserving.**
- `/provider` opens the same in-tab provider switcher as `Ctrl+B`.
- The command is listed with app-level slash commands, alongside `/config`.
- `/provider` is ignored while a provider turn is busy, matching the `Ctrl+B`
  guard against swapping provider mid-stream.
- `codexHandshake` includes `params["model"] = args.Model` on
  `thread/start`/`thread/resume` when a model is selected.

**Key files.**
- `provider.go` (`appBuiltinSlashCmds`)
- `update.go` (`handleCommand`)
- `codex.go` (`codexHandshake`)
- `update_test.go`
- `codex_handshake_test.go`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run 'HandleCommand_Provider|CodexHandshake_SendsSelectedModel'`

**Rebase risk.** Medium. Provider switching and Codex app-server protocol code
are both active areas. Re-check generated Codex schemas after app-server
updates; `ThreadStartParams` and resume params must still accept `model`.

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
