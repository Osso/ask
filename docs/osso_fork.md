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
734f5d5 main: add CLI option validation, reject unknown flags/subcommands
bd2ca81 Add --simulate-approval flag to exercise the approval modal at startup
ae42218 selection: copy to clipboard on mouse release
f0a6d4d input: copy selection on Ctrl+C when one is active
3419906 Show Codex hook output
05e132f Support run-plan for Claude provider
3c92873 Preserve Claude stop hook in ask
dbb7e43 tool_output: reclassify rg/fdfind unknown actions as search
ae7b11d codex: render commandExecution as parsed action rows
63399c7 tool_output: render call/result blocks with muted instead of dim
65ff469 themes: bump ayu foreground brighter and muted darker
c83f1d4 tabs: layer Ctrl+D so it backs out instead of force-quitting
1933591 tabs: rebind new-tab to Ctrl+N and normalize Ctrl+arrow matching
5af39f4 themes: soften ayu string highlights
08c4dbf themes: use codex ayu yellow for highlights
23cc740 Run selected slash completion on Enter
b53906b Mirror Codex compact command
30d2799 Mirror Codex run-plan command
b878840 Normalize Bubble Tea key matching
52ba41b Add provider switch command
45e6f06 Render tool outputs in stream and session replay
72671c8 themes: add ayu (mirage) palette
c2bebc7 claude: route permission prompts to claude-bash-hook-approval MCP
99c7f71 deploy: local build+test+install script
27915bd mcp: enforce 1 MiB argument size limit on tool handlers
200be4a worktree: fix TOCTOU in ensureWorktreeGitignore via Lstat+atomic rename
f163dea shell: strip Anthropic credentials from shell subprocess env
86b8675 mcp: disable localhost bypass for DNS-rebinding protection
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

## 2. Subprocess credential filtering

**Purpose.** Neither shell mode nor the Claude provider should inherit
Anthropic credentials from the host shell. Shell mode runs arbitrary user
commands and must not leak the keys; the Claude provider must let claude
fall back to its stored subscription credentials so a user-exported
`ANTHROPIC_API_KEY` does not silently bill the API instead of the Pro/Max
plan.

**Behavior details worth preserving.**
- Shell commands use `cmd.Env = shellEnv()` instead of inheriting `os.Environ`
  implicitly. `shellEnv` strips `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`,
  `ANTHROPIC_BASE_URL`, `CLAUDE_APP_SECRET`, and `MCP_TIMEOUT`.
- The Claude provider's `claudeEnv` strips `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`, and `ANTHROPIC_BASE_URL` from the inherited
  environment. Without this, an exported `ANTHROPIC_API_KEY` (common when
  the user runs ask from inside another Claude Code session) takes
  precedence over the subscription auth that claude reads from
  `~/.claude.json`.
- Ollama mode strips first, then re-injects `ANTHROPIC_BASE_URL` and
  `ANTHROPIC_AUTH_TOKEN=ollama`, so a stale host token cannot leak into
  the local ollama route either.
- The rest of the environment is preserved so normal shell-mode and
  provider behavior remain unchanged.

**Key files.**
- `shell.go` (`startShellCmd`, `shellEnv`)
- `claude.go` (`claudeEnv`)

**Tests to re-run after rebase.**
- `go test ./...`
- Focused: `go test ./... -run 'ClaudeEnv|ShellEnv'`

**Rebase risk.** Low to medium. Upstream shell-mode work often touches
`startShellCmd`; make sure future refactors do not accidentally return to
implicit environment inheritance. Same applies to `claudeEnv` — any
upstream change that swaps it for a plain `os.Environ()` reintroduces the
API-key-over-subscription regression.

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

## 4. Claude permission approval orchestration

**Purpose.** ask owns the approval modal UI; claude-bash-hook is consulted
as a subprocess for codex-driven SAFE/UNSAFE/UNSURE classification. ask is
the only path to the user; bash-hook never speaks MCP back to Claude in
this flow, which removes the elicitation/-p deadlock that the previous
direct-routing version hit on UNSURE.

**Behavior details worth preserving.**
- Claude CLI argv passes
  `--permission-prompt-tool mcp__ask__approval_prompt` (the embedded ask
  MCP bridge), not the bash-hook MCP server.
- `mcp.go::approvalTool` consults ask's `alwaysAllow` cache first, then
  shells out to `claude-bash-hook-approval decide` (5s timeout,
  `exec.LookPath` fallback). SAFE auto-allows, UNSAFE auto-denies with the
  codex reason, UNSURE/error/missing-binary falls through to the modal.
- `decideViaBashHook` payload includes `tool_name`, `input`, `cwd`,
  `permission_suggestions`, and `blocked_path` so the SDK hints reach
  codex via bash-hook's `build_prompt`.
- `buildDenyBody(message)` produces the `{behavior:"deny",message:...}`
  body for the UNSAFE branch.
- The approval modal carries an optional feedback text input (Tab past
  "Always allow" to focus it). On `approvalChoiceAlways` with non-empty
  feedback, ask spawns `claude-bash-hook-approval persist-rule`
  fire-and-forget so codex generalizes the feedback into TOML rules,
  caches the narrow rule in `alwaysAllow` for this session, and returns a
  plain `allow` body to claude with **no** `updatedPermissions` (the
  persisted bash-hook rule covers future invocations across sessions). On
  empty feedback the previous session-scoped `addRules` path is
  unchanged.
- `persistRuleFunc` is a package-level var (defaults to `runPersistRule`)
  so tests can swap in a recorder without spinning up a real subprocess.
- Standalone Claude Code (no ask) keeps using
  `mcp__claude-bash-hook-approval__approval_prompt` directly via shell
  alias; that path is unchanged.

**Key files.**
- `claude.go` (`claudeCLIArgs`)
- `mcp.go` (`approvalIn`, `approvalTool`, `handleApprovalReply`,
  `decideViaBashHook`, `runPersistRule`, `persistRuleFunc`,
  `buildDenyBody`)
- `approval.go` (`updateApprovalButtons`, `updateApprovalFeedback`,
  `sendApproval`, `viewApproval`, `approvalFeedbackRow`)
- `claude_cli_test.go`
- `mcp_test.go` (`TestDecideViaBashHook_*`, `TestApprovalTool_BashHook*`,
  `TestHandleApprovalReply_*`, `TestRunPersistRule_*`)
- `approval_test.go`

**Tests to re-run after rebase.**
- `go test ./... -run 'ClaudeCLIArgs|DecideViaBashHook|ApprovalTool|HandleApprovalReply|RunPersistRule|^TestApproval_'`
- `go test ./...`

**Rebase risk.** Medium-high. Touches both provider-argument construction
and the MCP bridge handler. Re-check (a) the exact `--permission-prompt-tool`
tool name, (b) that the bash-hook subprocess hand-off still uses the
`decide` and `persist-rule` argv subcommands and the JSON wire shapes
documented in `approval_prompt.rs::run_decide_cli` /
`run_persist_rule_cli`, and (c) that the 5s `decide` timeout + LookPath
fallback both still degrade to the modal rather than failing the call.

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

**Purpose.** Add an `ayu` theme matching Ayu Mirage's dark blue-grey base
with Ayu/Codex yellow accents and foreground highlights.

**Behavior details worth preserving.**
- `ayuTheme()` is registered in `themeRegistry`.
- The theme sets explicit background and foreground colors, not only accent
  colors, so terminal OSC 10/11 matches the palette.
- Row and inline-code highlights use Ayu/Codex yellow (`#E6B450`) as foreground text
  instead of the Mirage blue-grey background role.
- Code-block string tokens use Ayu cyan/teal (`#95E6CB`) instead of the brighter
  success yellow-green, while keeping no token-specific background.

**Key files.**
- `themes.go` (`themeRegistry`, `ayuTheme`)
- `view.go` (`buildGlamourStyle`)

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

## 8. Built-in command mirrors (`/run-plan`, `/compact`)

**Purpose.** Show and handle selected built-in TUI commands inside ask.
Codex app-server exposes `skills/list`, but it does not expose a built-in slash
command list, and Claude has no slash command list at all, so ask mirrors the
commands it needs locally. `/run-plan` works for both Codex and Claude;
`/compact` is Codex-only.

**Behavior details worth preserving.**
- `/run-plan` appears in both `claudeProvider.BaseSlashCommands()` and
  `codexProvider.BaseSlashCommands()`.
- `/run-plan` reads `PLAN.md`; `/run-plan <file>` reads the supplied plan file.
- The first unchecked `- [ ]` or `* [ ]` item is submitted as a generated
  prompt that includes "Commit after completing this item. Check it off
  (change `- [ ]` to `- [x]`). Do not delete existing items from <file>."
- `PLAN_FILE` is set to the absolute path of the plan file (e.g.
  `/home/osso/Repos/ask/PLAN.md`), not `1`. Matches the hook contract for
  `claude-plan-hook` / Codex's stop hook so the hook can re-read the plan
  without guessing the cwd.
- When no pending item exists, ask reports `No pending tasks in <file>.` and
  does not start a provider turn.
- If a provider process is already running, `handleRunPlan` calls
  `m.killProc()` before `sendToProvider` so the new subprocess inherits the
  freshly-set `PLAN_FILE` env var. Without this, the env update reaches the
  parent only and the still-running child keeps the old value.
- `/compact` appears in Codex's base slash-command list.
- `/compact` requires an active Codex app-server thread and sends
  `thread/compact/start` with the current `threadId`.
- When no Codex thread exists, ask reports `No Codex session to compact.` and
  does not start a provider turn.

**Key files.**
- `claude.go` (`claudeProvider.BaseSlashCommands`)
- `codex.go` (`codexProvider.BaseSlashCommands`, `codexRunPlanPrompt`,
  `codexFindNextPlanItem`, `codexStartCompaction`)
- `update.go` (`handleCommand`, `handleRunPlan`, `handleCodexCompact`)
- `codex_skills_test.go`
- `provider_test.go`
- `update_test.go`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run 'ClaudeProvider_Metadata|CodexBaseSlashCommandsIncludes|CodexFindNextPlanItem|CodexRunPlanPrompt|HandleCommand_CodexRunPlan|HandleCommand_ClaudeRunPlan|HandleCommand_CodexCompact'`

**Rebase risk.** Medium. If upstream Codex changes `/run-plan` wording,
`PLAN_FILE` semantics, `/compact` dispatch, or app-server grows a built-in
command API, update or remove ask's mirrors instead of letting behavior
drift. Watch for upstream re-introducing a Codex-only `/run-plan` guard in
`handleCommand`; `handleRunPlan` is intentionally provider-agnostic.

---

## 9. Slash completion Enter dispatch

**Purpose.** Pressing Enter while the slash-command picker is open should run
the highlighted completion, not submit the partially typed prefix as an unknown
command.

**Behavior details worth preserving.**
- Tab still accepts the highlighted slash command into the input without
  submitting it.
- Enter behavior depends on how many completions match:
  - **Exactly one match** — Enter promotes that completion to the submitted
    line and dispatches it directly (e.g. `/comp` ⇒ `/compact` runs without a
    second Enter). Recorded into `inputHistory` as the resolved command.
  - **Multiple matches with no exact hit** — Enter accepts the highlighted
    entry into the input (same as Tab) without submitting; the user gets to
    refine before the next Enter.

**Key files.**
- `update.go` (`KeyEnter` handling in `updateInput`)
- `update_test.go`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run 'FilterSlashCmds|LockModifiers|EnterAcceptsSlashCompletion'`

**Rebase risk.** Medium. This sits in the shared input key dispatcher; re-check
if upstream changes slash picker semantics, history recording, or path-picker
Enter handling. Watch for upstream simplifying back to a single Tab-only
accept path.

---

## 10. Layered Ctrl+D

**Purpose.** Make `Ctrl+D` act like a progressive "back out" key: leave shell
mode first, then close the active tab if there is more than one, and only quit
the whole app on the last tab. Avoids accidentally killing every tab when the
user just meant to exit shell mode.

**Behavior details worth preserving.**
- The app wrapper intercepts `Ctrl+D` before dispatching to the active tab.
- `Ctrl+D` matching accepts all Bubble Tea v2 forms: `ModCtrl + 'd'`,
  `msg.String()`/`msg.Keystroke()` equal to `ctrl+d`, and the raw control-code
  shape (`0x04`).
- Order of operations in `app.handleCtrlD`:
  1. If the active tab is in shell mode, call `exitShellMode()` on it and
     return; no tab is closed and the app does not quit.
  2. Otherwise, if more than one tab is open, close the active tab via
     `closeTab(active.id)` so neighbours stay alive.
  3. Otherwise, fall through to `quit`, which drains pending replies, kills
     provider and shell subprocesses, and stops each tab's MCP bridge before
     returning `tea.Quit`.
- `quit` also arms `a.quitting` / `a.quittingVID` from the active tab's
  `virtualSessionID` (mirroring the last-tab branch in `closeTab`), so
  Ctrl+D on the last tab reaches the same inline "last session: …"
  exit screen as closing the final tab. Empty VS id leaves the flag
  disarmed and the altscreen exit silent.
- `Ctrl+C` twice on an empty idle prompt remains the tab-close path.

**Key files.**
- `util.go` (`isCtrlKey`)
- `tabs.go` (`app.Update`, `app.handleCtrlD`, `app.quit`)
- `tabs_test.go`
- `README.md`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run TestApp_CtrlD`

**Rebase risk.** Low to medium. This lives in the app-level key dispatcher;
re-check if upstream changes tab lifecycle, quit handling, or cleanup paths.
If `exitShellMode` moves or changes signature, update `handleCtrlD` accordingly.

---

## 11. Provider switch command and Codex model forwarding

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

## 12. Ctrl+N opens a new tab

**Purpose.** Match the common terminal/browser shortcut for "new tab" and avoid
clashing with the Ctrl+T transpose-character binding shells/readline use.

**Behavior details worth preserving.**
- The app wrapper opens a new tab on `ModCtrl + 'n'`, not `ModCtrl + 't'`.
- README, `/config` provider-picker comment, and `tabs.go` docstring all refer
  to `Ctrl+N` as the new-tab shortcut.

**Key files.**
- `tabs.go` (`app.Update`)
- `config_modal.go` (`openConfigProviderPicker` comment)
- `config_provider_test.go` (`TestOpenTab_LoadsCfgFromDisk` comment)
- `tabs_test.go` (`TestApp_CtrlNOpensNewTab`, `TestApp_CtrlTDoesNotOpenNewTab`)
- `README.md`

**Tests to re-run after rebase.**
- `go test ./... -run 'TestApp_CtrlN|TestApp_CtrlTDoesNotOpenNewTab'`
- `go test ./...`

**Rebase risk.** Low. Watch for upstream key dispatcher changes that re-add
`Ctrl+T` handling, and update README/comments together with the binding.

---

## 13. Codex commandExecution action-based rendering

**Purpose.** Replace the noisy `▸ shell` + `command: /usr/bin/zsh -lc 'git
log -6'` block with Codex-style parsed-action rows so the transcript shows the
actual intent (`▸ git log -6`, `▸ read main.go`, `▸ search TODO in src/`)
instead of the shell wrapper Codex used to invoke the command.

**Behavior details worth preserving.**
- `codexCommandActions` extracts the `commandActions` array off a Codex
  `commandExecution` item; missing or malformed entries are skipped and an
  empty/missing array returns nil so the renderer falls back cleanly.
- `toolCallMsg.actions` carries the parsed actions through to the UI; Claude
  tool calls leave it nil so they keep the generic key/value rendering.
- `update.go` dispatches to `renderToolCallActionsBlock` whenever
  `len(msg.actions) > 0`, otherwise to `renderToolCallBlock`.
- A single action collapses into the header itself: `▸ git status`,
  `▸ read main.go`. Multiple actions get a `▸ shell` header with each action
  as an indented row.
- Consecutive `read` actions fold into one comma-joined row with duplicate
  names dropped (`read a.go, b.go`), matching Codex's exec_cell grouping.
- Action titles stay lowercase (`read`, `list`, `search`) per the user's
  request; `unknown` actions extract the program token as the title so the
  user sees `git log -6` rather than `run git log -6`.
- `unknown` actions whose program is a known search tool (`rg`, `fd`,
  `fdfind`, `ag`, `ack`) are reclassified as `search`, since codex itself
  only tags `grep`. The args are parsed positionally (flag tokens dropped,
  matched single/double quotes preserved) and the row reads `search
  <query> in <path>` — falling back to the raw command when neither can
  be inferred. Space-separated flag values (`rg -t go TODO src/`) over-eat
  the next positional; that limitation is locked in by test.

**Key files.**
- `codex.go` (`codexToolOutputMsgs`, `codexCommandActions`)
- `tool_output.go` (`renderToolCallActionsBlock`, `compactCommandActions`,
  `renderSingleCommandAction`, `splitProgramAndArgs`, `searchPrograms`,
  `isSearchProgram`, `parseSearchArgs`, `splitShellTokens`, `searchBody`)
- `tool_output_test.go`
- `types.go` (`toolCallMsg.actions`)
- `update.go` (`toolCallMsg` dispatch)
- `update_test.go`

**Tests to re-run after rebase.**
- `go test ./...`
- Focused:
  `go test ./... -run 'RenderToolCallActions|CodexCommandActions|SplitProgramAndArgs|ToolCallMsgWithActions|UnknownSearchTool|UnknownNonSearch'`

**Rebase risk.** Medium. Tied to the Codex v2 `commandExecution` schema —
re-check after Codex app-server schema updates that may rename
`commandActions` or change the `CommandAction` discriminator. Touches the
shared `toolCallMsg` struct, so any upstream tool-call wiring change needs to
preserve the `actions` field.

---

## 14. Claude Stop hook registration

**Purpose.** Claude's `Stop` event fires when the assistant turn ends. Ask
registers a Stop hook so an external binary (`claude-plan-hook`) can read
`PLAN_FILE`, decide whether the next plan item is ready, and queue follow-up
work without the user manually invoking `/run-plan` again.

**Behavior details worth preserving.**
- `claudeHookSettings` registers a `Stop` entry in the hooks JSON alongside
  the existing `PreToolUse`, `SubagentStart`, and `SubagentStop` entries.
- The `Stop` hook command points at `/home/osso/.cargo/bin/claude-plan-hook`
  (absolute path, not a `_hook` subcommand). Other hooks still POST back to
  the embedded ask bridge via `_hook <event> --port <mcpPort>`; Stop is the
  only one that delegates to a separate binary.
- The other registered hooks remain wired through `hookCmd(...)` so port
  routing keeps working when the Stop binary is not installed.

**Key files.**
- `claude.go` (`claudeHookSettings`)
- `claude_cli_test.go` (`TestClaudeHookSettings_RegistersSubagentHooks` —
  asserts the Stop entry plus `claude-plan-hook` substring)

**Tests to re-run after rebase.**
- `go test ./... -run ClaudeHookSettings`
- `go test ./...`

**Rebase risk.** Low to medium. The hook registration table is short and
upstream rarely touches it, but if Claude renames the `Stop` event or the
embedded bridge gains its own plan-aware hook, this delegation should be
revisited rather than carried forward unchanged. The absolute path to
`claude-plan-hook` is environment-specific and should stay matched to the
local Cargo install path.

---

## 15. Codex hook output rendering

**Purpose.** Codex emits `hook/completed` notifications for user-defined
hooks (Stop, PreCompact, etc.). Show the hook's text entries in the
transcript so the user can see why a hook blocked, what the next plan item
is, or what Codex routed back from the hook script — instead of silently
swallowing the event.

**Behavior details worth preserving.**
- `codexHookOutputMsg` extracts `params.run.entries[]` from a `hook/completed`
  event. Each entry's `text` is trimmed; empty entries are skipped.
- An entry kind of `error` or `stop` flips the rendered block to error style;
  a run `status` of `failed`, `blocked`, or `stopped` does the same. This
  matches the Codex client's own treatment of stop-hook output.
- The rendered block uses the dedicated `renderHookOutputBlock` (header
  `▸ <eventName> hook` plus indented text rows) — distinct from
  `renderToolResultBlock` so the user can tell hooks apart from tool calls
  in scrollback.
- Hook output respects the same line-clamping (`clampToolOutput`) as tool
  output but bypasses the `quietMode` / `toolOutputMode` gates: hook
  feedback is treated as control-flow signal and always rendered, even when
  tool output is muted (`TestUpdate_HookOutputMsgAlwaysRenders`).
- Cross-tab safety: the update handler drops the message when
  `msg.proc != m.proc`, matching the same guard used for `toolResultMsg`.

**Key files.**
- `codex.go` (`codexEventToMsgs` `hook/completed` case, `codexHookOutputMsg`)
- `tool_output.go` (`renderHookOutputBlock`)
- `types.go` (`hookOutputMsg`)
- `update.go` (`hookOutputMsg` case in `Update`)
- `codex_stream_test.go` (`TestCodexEventToMsgs_HookCompletedEmitsHookOutput`)
- `update_test.go` (`TestUpdate_HookOutputMsgAlwaysRenders`)

**Tests to re-run after rebase.**
- `go test ./... -run 'HookCompleted|HookOutput'`
- `go test ./...`

**Rebase risk.** Medium. Tied to the Codex app-server `hook/completed`
schema. If upstream adds first-class hook rendering, prefer dropping this
patch over carrying a parallel implementation. Watch for entry-kind or
status-string renames that would silently disable the error styling.

---

## 16. Selection clipboard ergonomics

**Purpose.** Make ask's mouse selection feel like a normal terminal selection
even though ask owns the mouse capture (and therefore terminal-emulator copy
bindings cannot see the selection).

**Behavior details worth preserving.**
- Mouse-release with a non-degenerate selection writes the selected text to
  the system clipboard immediately via `copyTextCmd`. No keystroke required.
  Matches the copy-on-select UX users already get from ghostty/kitty's own
  `copy-on-select` option.
- The selection highlight is **kept visible** after release (`selActive=true`,
  no `clearSelection`) so the user sees what was just copied. Click-drag
  again or click-no-drag to clear.
- Ctrl+C with an active selection routes to `copySelectionAndClear` (clears
  the highlight on copy). This is the explicit copy-and-clear shortcut and
  serves as a backup path if the release-time copy was somehow missed.
- Ctrl+C also resets `exitArmed` on the copy path so a stale "press ctrl+c
  again to exit" arm from before the selection does not close the tab on the
  next press.
- Ctrl+C with no selection falls through to the existing cancel-turn /
  arm-exit / clear-input ladder unchanged.
- Note: terminal emulators may consume Ctrl+C themselves (e.g. ghostty's
  `ctrl+c=copy_to_clipboard` binding can swallow the keystroke even with a
  `performable:` prefix when ask owns the mouse). Copy-on-release is the
  load-bearing path; the Ctrl+C handler is best-effort and only fires when
  the keystroke actually reaches ask.

**Key files.**
- `update.go` (`tea.MouseReleaseMsg` handler, `updateInput` Ctrl+C branch)
- `selection.go` (`copySelectionAndClear`, `copyTextCmd`)
- `selection_test.go` (`TestUpdateMouseRelease_FinalizesSelectionAndCopies`,
  `TestUpdateCtrlC_WithSelectionCopiesAndClears`,
  `TestUpdateCtrlC_NoSelectionFallsThroughToCancel`,
  `TestUpdateCtrlC_NoSelectionEmptyInputArmsExit`)

**Tests to re-run after rebase.**
- `go test ./... -run 'MouseRelease|UpdateCtrlC|CopySelection|RightClick'`
- `go test ./...`

**Rebase risk.** Medium. Upstream may evolve the selection or
mouse-handling layer (ScrollbarDragging, viewport mouse capture, modal
dismissal), so re-check the MouseReleaseMsg branch carefully — both the
copy trigger and the deliberate omission of `clearSelection` need to
survive. If upstream introduces its own copy-on-select toggle, prefer
delegating to that and dropping this patch.

---

## 17. Simulated approval flag

**Purpose.** Provide a developer-facing CLI flag that fires a synthetic
`approvalRequestMsg` directly into the running tea program at startup so the
approval modal UI can be exercised without spawning a provider subprocess or
an MCP round-trip.

**Behavior details worth preserving.**
- `--simulate-approval` (no value) defaults to `Bash`; `--simulate-approval=<tool>`
  picks an arbitrary tool name. `parseSimulateApprovalFlag` strips the flag from
  argv before the rest of the CLI parser sees it, so the flag does not interfere
  with `resume`/`help` parsing.
- After the tea.Program is constructed, `injectSimulatedApproval` waits ~150ms
  (so the program is in its main loop) and then calls `p.Send(approvalRequestMsg{...})`
  with a buffered reply channel and a `simulated-approval` tool-use id.
- `simulatedApprovalInput` produces a tool-shaped argument map for known tool
  names (Bash command, Edit/Write file_path, Glob pattern, WebFetch url, etc.)
  so the modal renders representative content for each tool.
- The flag is documented in the help text printed by `printHelp`.

**Key files.**
- `main.go` (`parseSimulateApprovalFlag`, `injectSimulatedApproval`,
  `simulatedApprovalInput`, `printHelp`)
- `main_test.go` (`TestParseSimulateApprovalFlag`,
  `TestSimulatedApprovalInput_TargetsKnownTools`,
  `TestPrintHelp_MentionsKeyCommands`)

**Tests to re-run after rebase.**
- `go test ./... -run 'SimulateApproval|SimulatedApprovalInput|PrintHelp'`
- `go test ./...`

**Rebase risk.** Low. The injection path depends on the `approvalRequestMsg`
shape and `tea.Program.Send`. If upstream renames the approval message type or
moves the tab/program lifecycle, update `injectSimulatedApproval` accordingly.

---

## 23. Symlink-resolved cwd in bash-hook approval payload

**Purpose.** `decideViaBashHook` and `runPersistRule` send `cwd` to
`claude-bash-hook-approval`, which pre-classifies file-edit tools as
SAFE iff `target.starts_with(cwd)`. Claude's subprocess sees the
canonical cwd via `getcwd(2)` (the kernel resolves `cmd.Dir`
symlinks) and reports tool input file paths in canonical form. ask's
`os.Getwd` returns the literal `$PWD` form when it stat-matches `.`,
so without this patch a symlinked workspace produces a literal cwd
that fails the `starts_with` check against the canonical target,
demoting otherwise-in-scope worktree edits to Unsure and surfacing
the approval modal.

**Behavior details worth preserving.**
- `bashHookCwd()` calls `os.Getwd` then `filepath.EvalSymlinks` and
  returns the resolved form; on error it falls back to the literal
  cwd, and on a getwd failure it returns "" (caller's bash-hook
  process then uses its own cwd).
- Both `decideViaBashHook` and `runPersistRule` use the helper so the
  decide path and the persist-rule path agree on which cwd they're
  asserting against.
- Plain (non-symlinked) cwds round-trip unchanged — `EvalSymlinks` is
  a no-op when the path has no symlinks.

**Key files.**
- `mcp.go` (`bashHookCwd`, `decideViaBashHook`, `runPersistRule`)
- `mcp_test.go` (`TestBashHookCwd_ResolvesSymlink`,
  `TestDecideViaBashHook_PayloadCwdIsSymlinkResolved`)

**Tests to re-run after rebase.**
- `go test ./... -run 'BashHookCwd|DecideViaBashHook|RunPersistRule'`
- `go test ./...`

**Rebase risk.** Low. Pure helper added in `mcp.go`; both call sites
were already issuing `os.Getwd` and just route through it now.

---

## 22. Ctrl+left/right word motion in the textarea

**Purpose.** Bubbles' textarea defaults bind word motion only to
`alt+left` / `alt+right`, but most terminals (kitty, ghostty,
gnome-terminal, etc.) emit `ctrl+left` / `ctrl+right` for word jumps.
Without this patch those keys insert literal characters or no-op,
which feels broken in any normal editing flow.

**Behavior details worth preserving.**
- `newTab` extends the textarea KeyMap with extra keys after
  `textarea.New()` returns:
  - `WordForward`: `alt+right`, `alt+f`, `ctrl+right`
  - `WordBackward`: `alt+left`, `alt+b`, `ctrl+left`
  - `DeleteWordBackward`: `alt+backspace`, `ctrl+w`, `ctrl+backspace`
  - `DeleteWordForward`: `alt+delete`, `alt+d`, `ctrl+delete`
- The bubbles defaults stay in the binding so existing alt-based
  muscle memory keeps working.
- `InsertNewline` continues to be customised right above (this patch
  shares the same block, so they should be re-applied together when
  rebasing).
- The app-level tab dispatcher in `tabs.go` was rebound from
  `ctrl+left` / `ctrl+right` to `ctrl+shift+pgup` / `ctrl+shift+pgdown`
  in the same patch series. Without that move, the textarea bindings
  above are dead because `app.Update` consumes ctrl+left/right before
  the keypress reaches the active tab. `isCtrlShiftSpecial` (in
  `util.go`) mirrors `isCtrlSpecial`'s tolerance for both modded-code
  and `String()`/`Keystroke()` shapes.

**Key files.**
- `main.go` (`newTab` textarea KeyMap setup)
- `tabs.go` (`app.Update` tab-switch dispatch on ctrl+shift+pgup/pgdown)
- `util.go` (`isCtrlShiftSpecial`)
- `main_test.go` (`TestNewTab_TextareaBindsCtrlWordMotion`)
- `tabs_test.go` (`TestApp_CtrlShiftPgUpPgDownSwitchesTabs`,
  `TestApp_CtrlLeftRightDoesNotSwitchTabs`)

**Tests to re-run after rebase.**
- `go test ./... -run 'TextareaBindsCtrlWordMotion|CtrlShiftPgUpPgDown|CtrlLeftRightDoesNotSwitch'`
- `go test ./...`

**Rebase risk.** Low. Only adds entries to existing KeyMap fields and
swaps the tab-switch key binding; if upstream renames or moves the
textarea KeyMap or the app-level tab dispatch, follow that rename.

---

## 21. Symlink-resolved Claude session dirs

**Purpose.** Claude encodes its on-disk session-file path from the
canonical (symlink-resolved) cwd via `getcwd(2)`. ask's
`os.Getwd` may instead return the unresolved form when `$PWD` is set
and stat-matches `.` — for example, when `ask resume` chdirs into a
workspace whose canonical path crosses a symlink (`/home/osso/Projects`
→ `/syncthing/Sync/Projects`). Without this patch the lookup encodes
the unresolved form and the session file isn't found.

**Behavior details worth preserving.**
- `claudeCandidateSessionDirs(cwd)` adds the `filepath.EvalSymlinks(cwd)`
  result as a second candidate when it differs from the literal cwd.
  Both the main `~/.claude/projects/<encoded>` dir and any
  `--claude-worktrees-<name>` siblings are enumerated for each form.
- The literal cwd's main dir stays first in the returned slice so the
  fallback in `claudeSessionPath` (`dirs[0].dir`) — used to construct
  error messages when nothing is found — keeps reporting the path the
  caller passed in.
- Sibling and main directories are de-duplicated across the two forms
  so a non-symlinked cwd keeps the same single-form behavior it had
  before.
- `EvalSymlinks` errors are non-fatal: ask continues with just the
  literal cwd's candidates. This keeps the function safe to call on a
  cwd that doesn't exist or has been pruned.

**Key files.**
- `session.go` (`claudeCandidateSessionDirs`)
- `session_test.go` (`TestClaudeCandidateSessionDirs_ResolvesSymlinkedCwd`)

**Tests to re-run after rebase.**
- `go test ./... -run 'ClaudeCandidateSessionDirs|ClaudeSessionPath|LoadClaudeHistory'`
- `go test ./...`

**Rebase risk.** Low. Changes a single helper. If upstream rewrites
the encoding (e.g. switches to a hash, or starts storing the path in a
manifest), drop this patch in favor of the upstream lookup.

---

## 20. ask resume restores LastProvider

**Purpose.** `ask resume <vsID>` should reopen the conversation under the
provider that owned it last, not whatever the saved default config now
says. Resuming a Claude conversation while the saved default is Codex
previously dropped the user into Codex with no native session.

**Behavior details worth preserving.**
- `resumeLookup` returns `(id, workspace, lastProvider, err)`. The
  `LastProvider` field has been on `VirtualSession` for a while; the
  resume path now consumes it.
- In `main`, after `saveConfig(cfg)` runs (so we don't persist the
  override), the provider is resolved in this order: `--provider` flag,
  then `LastProvider` from the resumed VS, then the saved
  `cfg.Provider`. The flag always wins so users can force a different
  provider on a resumed conversation.
- `LastProvider` is run through `strictProviderByID`. If the stored id
  is no longer registered (provider removed/renamed), ask warns on
  stderr and falls back to the saved default rather than failing.
- VSes written before `LastProvider` was tracked have an empty string
  for that field; `resumeLookup` returns "" and the saved default is
  used, matching the prior behavior.

**Key files.**
- `main.go` (`resumeLookup`, `main` resume dispatch)
- `main_test.go` (`TestResumeLookup_FindsVSAndReturnsWorkspace`,
  `TestResumeLookup_ReturnsLastProviderForCodexVS`,
  `TestResumeLookup_LegacyVSWithoutLastProviderReturnsEmpty`)

**Tests to re-run after rebase.**
- `go test ./... -run ResumeLookup`
- `go test ./...`

**Rebase risk.** Low. The only signature change is `resumeLookup`'s
extra return. If upstream introduces its own provider-aware resume
flow, prefer dropping this patch over carrying a parallel
implementation.

---

## 19. CLI provider/model overrides

**Purpose.** Let users pick the provider and/or model for a single launch
without editing config or opening the TUI's `/config` / `/provider` /
`/model` flow. Useful for shell aliases, scripted launches, and
ad-hoc model swaps.

**Behavior details worth preserving.**
- `--provider <id>` and `--provider=<id>` set `cfg.Provider` for the
  current run only. Unknown ids exit with status 2 and an error listing
  the registered ids; `strictProviderByID` does the lookup so the
  fallback-to-first behavior of `providerByID` does not silently mask
  typos. The override is applied **after** `saveConfig(cfg)` runs so it
  never persists.
- `--model <name>` and `--model=<name>` set `first.providerModel` after
  `newTab` returns, so the override beats the provider's saved
  settings without modifying them. No validation: the picker already
  supports `AllowCustom`, so any string the picker would accept is
  legal here too.
- Bare `--provider` / `--model` (no value) and empty `--provider=` /
  `--model=` are validation errors so a typo doesn't silently swallow
  the next positional.
- `parseProviderModelFlags` runs before `parseCLICommand` (alongside
  `parseSimulateApprovalFlag`), so the new flags don't trip the
  unknown-option check.
- Help text in `printHelp` documents both flags under an `Options:`
  section.

**Key files.**
- `main.go` (`parseProviderModelFlags`, `strictProviderByID`,
  `registeredProviderIDs`, `printHelp`, `main` dispatch)
- `main_test.go` (`TestParseProviderModelFlags`,
  `TestStrictProviderByID`, `TestPrintHelp_MentionsKeyCommands`)

**Tests to re-run after rebase.**
- `go test ./... -run 'ParseProviderModelFlags|StrictProviderByID|PrintHelp'`
- `go test ./...`

**Rebase risk.** Low. The override path is contained to `main()` plus
two helpers. If upstream changes how `cfg.Provider` is consumed by
`newTab`, or how `providerModel` is stored on the model, re-check that
the override still lands before the first provider session starts.

---

## 24. CLI worktree override

**Purpose.** Let users enable worktree mode for a single launch without
opening `/config` or editing `ask.json`. Useful for shell aliases that want
worktree isolation regardless of the persisted default.

**Behavior details worth preserving.**
- `-w` and `--worktree` both set `cfg.UI.Worktree = &true` for the current
  run. There is no value form and no `--no-worktree` opt-out: when the flag
  is absent the saved config is the source of truth.
- The override is applied **after** `saveConfig(cfg)` runs so it never
  persists, matching the `--provider` / `--model` precedent.
- The override lands **before** the startup `ensureWorktreeGitignore()`
  guard so the `.gitignore` is set up for this run when the flag flips a
  previously-disabled config to enabled.
- New tabs spawned in-session (Ctrl+N) call `loadConfig()` fresh, so they
  see the saved value, not the launch override. This matches `--provider` /
  `--model` and is intentional — persistence is what `/config` is for.
- `parseWorktreeFlag` runs before `parseCLICommand`, alongside
  `parseSimulateApprovalFlag` and `parseProviderModelFlags`, so the new
  flag does not trip the unknown-option check.
- Help text in `printHelp` documents `-w, --worktree` under the
  `Options:` section.

**Key files.**
- `main.go` (`parseWorktreeFlag`, `printHelp`, `main` dispatch)
- `main_test.go` (`TestParseWorktreeFlag`,
  `TestPrintHelp_MentionsKeyCommands`)

**Tests to re-run after rebase.**
- `go test ./... -run 'ParseWorktreeFlag|PrintHelp'`
- `go test ./...`

**Rebase risk.** Low. Self-contained to `main()` and one helper. If
upstream restructures how `cfg.UI.Worktree` is consumed (or splits the
`uiConfig` struct), re-check that the override still lands before the
first `ensureWorktreeGitignore()` call and before `newTab` reads the
config.

---

## 25. OSC 7 cwd reporting in worktree mode

**Purpose.** Tell the host terminal emulator (kitty, ghostty, wezterm,
gnome-terminal, …) which directory to use as the "current cwd" so a
new tab/split opened from the same window inherits the active ask
tab's worktree path instead of the project root. Without this, a user
running ask in worktree mode and opening a fresh terminal tab lands
in the repo root every time, even though the live conversation is
operating inside `.claude/worktrees/<name>`.

**Behavior details worth preserving.**
- `emitTermCwd(path)` writes a single OSC 7 sequence
  (`ESC ] 7 ; file://<host>/<path> ESC \\`) directly to `/dev/tty` so
  Bubble Tea's renderer cannot interleave with the report. Empty path
  is a no-op.
- `emitTermCwdFunc` is a package-level var so tests can capture the
  emitted path without touching `/dev/tty`.
- `(*model).effectiveCwd()` returns `worktreePath(m.cwd, m.worktreeName)`
  when `worktreeName` is set, otherwise `m.cwd`. This is the path
  reported via OSC 7.
- `(app).syncTermCwd()` emits the active tab's effective cwd; called
  once at startup from `main()` after `newApp(first)` so the very first
  render already reports the right path.
- `app.Update` wraps the existing dispatch (`dispatchUpdate`): it
  snapshots the active tab's effective cwd before, runs the dispatch,
  and emits OSC 7 only when the post-dispatch active tab's effective
  cwd has actually changed. This catches /cd, shell-mode cwd capture,
  /config worktree toggle, providerStartDoneMsg landing a new
  worktreeName on the active tab, tab open/focus/close events, and any
  future state mutation that flips effective cwd — without per-handler
  bookkeeping.
- A providerStartDoneMsg landing on an *inactive* tab does not emit,
  because the active tab's effective cwd has not changed.
- On exit the user's shell prompt naturally re-emits its own OSC 7,
  so no shutdown-time restore is needed.

**Key files.**
- `term_cwd.go` (`emitTermCwdFunc`, `emitTermCwd`, `writeOSC7ToTTY`,
  `(*model).effectiveCwd`, `(app).currentEffectiveCwd`,
  `(app).syncTermCwd`)
- `tabs.go` (`app.Update` diff wrapper, `app.dispatchUpdate`)
- `main.go` (one-shot `a.syncTermCwd()` after `newApp`)
- `term_cwd_test.go` (effectiveCwd selection, emitter stub plumbing,
  emit-on-active-change vs no-emit-on-inactive-change, tab-switch
  emit/no-emit)

**Tests to re-run after rebase.**
- `go test ./... -run 'TermCwd|EffectiveCwd|EmitTermCwd|SyncTermCwd|AppUpdate_Emits|AppUpdate_DoesNotEmit'`
- `go test ./...`

**Rebase risk.** Low to medium. The diff wrapper sits at the top of
`app.Update`. If upstream restructures the dispatch (e.g. moves
message routing into `tea.Model` interface helpers, splits app vs
tab updates, or adds a non-Update entry path that mutates
`m.worktreeName` / `m.cwd`), make sure each new entry path either
goes through `Update` or calls `a.syncTermCwd()` explicitly.
`writeOSC7ToTTY` writes to `/dev/tty`; environments without one
(CI, sandboxed runners) silently no-op, which is fine.

---

## 18. CLI option validation

**Purpose.** Reject unknown CLI flags and subcommands at startup with an error
message and the help text, instead of silently launching the TUI as if the user
had passed nothing. Earlier behavior swallowed typos (`ask --frobnicate`,
`ask banana`) without warning.

**Behavior details worth preserving.**
- `parseCLICommand(args []string) (cliCommand, error)` is the single entry
  point. It returns `Kind` of `"help"`, `"resume"`, or `"run"`, plus a `VSID`
  for resume.
- `--help`, `-h`, and bare `help` all map to `Kind: "help"`. Any extra
  arguments after a help token are an error.
- `resume` requires exactly one argument (the virtual session id); zero or
  more than one trailing arguments are an error.
- Any token starting with `-` that is not recognized produces
  `unknown option: <token>`.
- Any unrecognized leading positional argument produces
  `unknown argument: <token>`.
- `main` writes the error to stderr followed by the help text and exits with
  status 2 on validation failure.
- `parseSimulateApprovalFlag` runs before `parseCLICommand`, so
  `--simulate-approval[=<tool>]` is consumed without producing an
  `unknown option` error.

**Key files.**
- `main.go` (`cliCommand`, `parseCLICommand`, `main` dispatch)
- `main_test.go` (`TestParseCLICommand` covering: no args, empty slice, all
  three help forms, resume with vid, resume missing vid, resume too many args,
  help with extra arg, unknown long flag, unknown short flag, unknown
  subcommand)

**Tests to re-run after rebase.**
- `go test ./... -run 'ParseCLICommand|PrintHelp'`
- `go test ./...`

**Rebase risk.** Low to medium. If upstream adds a new subcommand or top-level
flag, it must be wired through `parseCLICommand` (and through
`parseSimulateApprovalFlag` if it shares the `--foo[=bar]` shape) or it will
be rejected as unknown. Touching this parser without a corresponding test
update will silently allow regressions.

---

## 26. pruneWorktrees resolves cwd symlinks

**Purpose.** Make worktree pruning actually clean up locked siblings when ask is
launched from a symlinked path (e.g. `~/Projects → /elsewhere/Projects` from a
Syncthing-style layout). Earlier, the lock-lookup map was keyed by git's
canonical (resolved-symlink) paths while the iteration paths were built from
the symlink-form `cwd`, so every lookup missed and `git worktree remove`
silently failed against still-locked trees, leaving stale-locked worktrees
piling up forever.

**Behavior details worth preserving.**
- `pruneWorktrees` calls `filepath.EvalSymlinks(cwd)` before any subsequent
  path manipulation. Failure falls through unchanged so non-symlinked cases
  stay byte-identical.
- Resolution happens before the worktree-name self-check, so being inside a
  symlinked worktree is detected correctly.
- Other call sites in `worktree.go` are unaffected — they shell out to git/jj
  which resolve symlinks internally; the lock-lookup map was the only
  read-side path comparison that needed canonicalisation.

**Key files.**
- `worktree.go` (`pruneWorktrees`)
- `worktree_test.go` (`TestPruneWorktrees_RemovesWhenCwdIsSymlinked`)

**Tests to re-run after rebase.**
- `go test ./... -run 'PruneWorktrees'`
- `go test ./...`

**Rebase risk.** Low. Pure addition at the top of `pruneWorktrees`. If upstream
restructures the prune flow (e.g. consolidates cwd derivation into a separate
helper, or moves the prune call into a different lifecycle hook), make sure
the symlink resolution still runs before any path is used as a lock-map key.

---

## 27. prepareProviderSessionAt resolves cwd symlinks

**Purpose.** When ask is launched from a symlinked checkout root
(`~/Projects → /elsewhere/Projects` from a Syncthing-style layout) the
provider session args (`rootCwd`, `args.Cwd`) carry the symlink-form
path. Claude/Codex call `getcwd(2)` and see the canonical form, and
the bash-hook approval helper compares file paths against `args.Cwd`,
so without this patch the two ends disagree and worktree-safe edits
get demoted from SAFE to Unsure (surfacing the approval modal). Resolve
via `filepath.EvalSymlinks` so ask, the provider, and the bash-hook
all see the same canonical paths.

**Behavior details worth preserving.**
- `prepareProviderSessionAt` resolves `rootCwd` at the top via
  `filepath.EvalSymlinks`; failure is non-fatal (falls through with the
  literal value).
- `args.Cwd` is resolved both when defaulted from `rootCwd` (no explicit
  cwd was passed) and when set directly (`resumeVirtualSession` handing
  a worktree path).
- The resume-recreate branch (`ensureResumeWorktree`) sees the resolved
  path so the worktree directory is recreated under its canonical name.
- Pairs with #23 (bash-hook payload cwd resolution) and #21 (Claude
  session-dir lookup): together those three patches ensure every
  cwd-derived path crossing the ask ↔ provider ↔ hook boundary is in
  canonical form.

**Key files.**
- `proc.go` (`prepareProviderSessionAt`)
- `worktree_lifecycle_test.go` (`TestEnsureProc_ResumeCanonicalizesSymlinkedRoot`)

**Tests to re-run after rebase.**
- `go test ./... -run 'EnsureProc|ResumeVirtualSession'`
- `go test ./...`

**Rebase risk.** Low. If upstream restructures `prepareProviderSessionAt`
or splits cwd derivation into a separate helper, ensure the
`EvalSymlinks` calls still run before any consumer reads `args.Cwd`.

---

## 28. Shell-wrapper summary in short Bash render

**Purpose.** Codex's own TUI renders shell-wrapper invocations like
`/usr/bin/zsh -lc 'cat foo.go'` as terse one-liners ("read foo.go").
ask mirrors that for short-mode Bash tool calls, the Bash approval modal
header, and the Codex `commandExecution` status string so the history
reads like a sequence of intent ("read X", "search Y in Z", "git log -6")
rather than a wall of zsh wrappers.

**Behavior details worth preserving.**
- `summarizeShellCommand` recognizes wrapper prefixes (`/usr/bin/zsh -lc`,
  `/bin/sh -c`, etc.) and unwraps the inner command before classifying.
- `cat <files>` → `read <files>` (multiple files comma-separated).
- `rg <pattern> <path>` / `fdfind …` → `search <pattern> in <path>`.
- Other commands fall through to the raw command text.
- `renderToolCallBlock` short-mode Bash collapses to a single
  `▸ <summary>` line when `summarizeShellCommand` returns non-empty;
  falls back to the previous header + key rows otherwise.
- `renderToolCallActionsBlock` renders each compacted action as its own
  `▸` line in short mode, matching the per-action explorer rows in the
  Codex TUI.
- `approvalSummary` uses the summary text so the Bash approval header
  reads the same way as the chat row.
- `codexItemStatus` prefixes `shell:` to the summary so live streaming
  shows the unwrapped intent before the call lands.

**Key files.**
- `tool_output.go` (`summarizeShellCommand`, `renderToolCallBlock`,
  `renderToolCallActionsBlock`)
- `approval.go` (`approvalSummary` Bash branch)
- `codex.go` (`codexItemStatus`)
- `tool_output_test.go` (updated `TestRenderToolCallBlock_ShortFiltersToAllowlist`,
  new `TestRenderToolCallBlock_ShortBashSummarizesWrapper`,
  `TestSummarizeShellCommand_ReadAndSearch`)
- `codex_stream_test.go` (updated `TestCodexEventToMsgs_ItemStartedStatusByType`)

**Tests to re-run after rebase.**
- `go test ./... -run 'SummarizeShellCommand|ShortBash|ShortFilters|StatusByType|ToolCallMsgShort'`
- `go test ./...`

**Rebase risk.** Medium. Cross-cuts four files and replaces an existing
render path. If upstream restructures `renderToolCallBlock` to take a
richer context object (mode + provider + tool defs), the Bash branch
needs reapplying. The summarizer itself is pure text manipulation — safe
to lift wholesale.

---

## 29. Allow bracketed paste during a turn

**Purpose.** Pasting into the composer while a provider turn is in flight
was silently dropped — the `tea.PasteMsg` handler gated on `!m.busy`,
even though typed keys land in the input regardless of busy state. The
inconsistency just lost the user's text. Now pastes fall through the
same composer path as typed keys.

**Behavior details worth preserving.**
- `tea.PasteMsg` handler keeps the `m.mode == modeInput` check (modal
  screens — session picker, ask question, approval, config, provider
  switch — still route paste through their own dispatchers below) but
  drops the `!m.busy` guard.
- Typed keys at `update.go:926` already run `m.input.Update(msg)`
  unconditionally, so this just brings the paste path into line.
- Image paste (`Ctrl+V` → `pasteImageCmd`) was already busy-tolerant at
  both the dispatch and `imagePastedMsg` handler; no change there.

**Key files.**
- `update.go` (`tea.PasteMsg` handler in `Update`)
- `update_test.go` (`TestUpdate_PasteMsgLandsInInputWhileBusy`)

**Tests to re-run after rebase.**
- `go test ./... -run 'PasteMsgLandsInInput'`
- `go test ./...`

**Rebase risk.** Low. Single-line gate change. Upstream issue is filed
(`Cidan/ask#1`); drop this patch if upstream merges its own fix.

---

## 30. Rewind conversation history

**Purpose.** Mirror Claude Code's "rewind" workflow for ask: pick a prior
user prompt, truncate the conversation to the point before it, restore that
prompt into the composer, and continue from a freshly materialized provider
session instead of only changing the visible transcript.

**Behavior details worth preserving.**
- `/rewind` is an app-level slash command available for every provider.
  Pressing Esc on an empty composer also enters rewind when there is at least
  one user prompt to restore; otherwise Esc keeps the close-tab confirmation
  behavior.
- The picker lists user prompts and defaults to the most recent one. Enter
  removes that prompt and all later entries from `m.history`, restores the
  selected prompt into `m.input`, kills the old provider process, and clears
  the old native session id.
- If retained history contains user/assistant turns, ask calls the provider's
  `Materialize` path via `translateVSCmd` and stores the returned native
  session id before the next send. Rewinding to the first prompt has no
  retained turns, so it starts from a clean provider session.
- While materialization is in flight, the restored prompt remains in the
  composer but Enter is ignored so the user cannot accidentally send before
  the forked native session id is ready.

**Key files.**
- `provider.go` (`/rewind` app slash command)
- `types.go` (`modeRollback`, `rollbackIdx`)
- `update.go` (`startRollbackPicker`, `updateRollback`, `applyRollback`)
- `proc.go` (`sendToProvider` materialization guard)
- `view.go` (`viewRollback`)
- `update_test.go` (`TestHandleCommand_RewindOpensRollbackPicker`,
  `TestRollbackEnterRestoresPromptAndMaterializesRetainedTurns`,
  `TestRollbackMaterializingBlocksSubmitWithoutAppendingUser`,
  `TestRollbackToFirstPromptStartsFreshSession`)

**Tests to re-run after rebase.**
- `go test ./... -run 'Rewind|Rollback'`
- `go test ./...`

**Rebase risk.** Medium. The feature depends on the virtual-session
materialization path staying provider-neutral. If upstream adds first-class
message rewind, prefer preserving the provider-session fork semantics over
keeping this exact picker implementation.

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
