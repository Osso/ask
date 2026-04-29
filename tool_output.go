package main

import (
	"encoding/json"
	"strings"
)

// Shared helpers for the "Tool Output" config tri-state: one gate,
// one pair of message types (toolCallMsg / toolResultMsg), and the
// renderers below. Per-provider extraction lives in claude.go and
// codex.go so each wire format stays in its own file.

// toolOutputMode is the user-visible tri-state for tool output rendering.
//
//	full  — show the call header, every input field, and the result body
//	       (including the "Command running in background with ID: …" ack
//	       Bash emits for run_in_background calls).
//	short — show the call header, only the highest-signal input fields
//	       per known tool (see shortToolFields), and the result body for
//	       foreground calls. Background-call results are suppressed.
//	off   — render nothing for tool calls or their results, not even
//	       headers.
type toolOutputMode string

const (
	toolOutputFull  toolOutputMode = "full"
	toolOutputShort toolOutputMode = "short"
	toolOutputOff   toolOutputMode = "off"

	toolOutputMaxLines = 20
	toolOutputMaxChars = 2000
)

// defaultToolOutputMode is what new installs and unrecognized values
// settle on — short keeps history readable without hiding tool activity
// entirely.
const defaultToolOutputMode = toolOutputShort

// parseToolOutputMode coerces a config string to a known mode. Empty
// or unrecognized values fall back to defaultToolOutputMode so a typo
// in ask.json never silences tool output completely.
func parseToolOutputMode(s string) toolOutputMode {
	switch toolOutputMode(s) {
	case toolOutputFull, toolOutputShort, toolOutputOff:
		return toolOutputMode(s)
	}
	return defaultToolOutputMode
}

// shortToolFields lists the input keys we surface for each known tool
// when the mode is "short". A tool not present here renders just the
// header in short mode — letting the user know something happened
// without dumping arbitrary input maps. New built-ins should be added
// here with their highest-signal field(s).
var shortToolFields = map[string][]string{
	// Claude built-ins.
	"Bash":         {"command"},
	"BashOutput":   {"bash_id"},
	"Edit":         {"file_path"},
	"ExitPlanMode": {"plan"},
	"Glob":         {"pattern"},
	"Grep":         {"glob", "output_mode", "pattern"},
	"KillBash":     {"shell_id"},
	"NotebookEdit": {"notebook_path"},
	"Read":         {"file_path"},
	"Task":         {"description"},
	"WebFetch":     {"url"},
	"WebSearch":    {"query"},
	"Write":        {"file_path"},
	// Codex tool surface (commandExecution becomes "shell").
	"shell": {"command"},
}

// filterShortInputs keeps only the allowlisted keys for the named tool
// in short mode. Tools without an allowlist entry get no input rows at
// all — that's the explicit signal "we don't know what's important
// here, so skip it".
func filterShortInputs(name string, input map[string]any) map[string]any {
	if len(input) == 0 {
		return input
	}
	allow, ok := shortToolFields[name]
	if !ok {
		return nil
	}
	out := make(map[string]any, len(allow))
	for _, k := range allow {
		if v, present := input[k]; present {
			out[k] = v
		}
	}
	return out
}

// nextToolOutputMode advances the tri-state for /config row cycling:
// full → short → off → full. Unknown values reset to the default so
// the picker never gets stuck on an invalid setting.
func nextToolOutputMode(cur toolOutputMode) toolOutputMode {
	switch cur {
	case toolOutputFull:
		return toolOutputShort
	case toolOutputShort:
		return toolOutputOff
	case toolOutputOff:
		return toolOutputFull
	}
	return defaultToolOutputMode
}

// shouldRenderToolCall decides whether a tool call goes into history.
// Quiet mode and "off" suppress everything; in any other mode the call
// header always renders so the user knows something fired. Background
// calls render too (as their command/inputs are still useful) — only
// the result ack is gated on full mode in shouldRenderToolResult.
func (m model) shouldRenderToolCall(_ toolCallMsg) bool {
	if m.quietMode || m.toolOutputMode == toolOutputOff {
		return false
	}
	return true
}

// shouldRenderToolResult decides whether a tool result goes into
// history. Background results are silenced in non-full modes — their
// payload is only the launch ack ("Command running in background with
// ID: …") and the actual completion arrives via task_notification.
func (m model) shouldRenderToolResult(msg toolResultMsg) bool {
	if m.quietMode || m.toolOutputMode == toolOutputOff {
		return false
	}
	if msg.background && m.toolOutputMode != toolOutputFull {
		return false
	}
	return true
}

// renderToolCallBlock formats a tool invocation as a history entry:
//
//	▸ Read
//	    file_path: /foo/bar.go
//
// input fields render as "key: value" rows under a dimmed style so the
// call stays distinct from the result that follows. Non-string inputs
// are JSON-encoded so arrays and nested maps remain legible. In short
// mode, the input map is filtered through shortToolFields so only the
// highest-signal keys per known tool show up.
func renderToolCallBlock(name string, input map[string]any, mode toolOutputMode) string {
	if mode == toolOutputShort {
		if name == "Bash" {
			if cmd, _ := input["command"].(string); cmd != "" {
				if summary := summarizeShellCommand(cmd); summary != "" {
					return outputStyle.Render(diffPathStyle.Render("▸ " + summary))
				}
			}
		}
		input = filterShortInputs(name, input)
	}
	header := diffPathStyle.Render("▸ " + nonEmpty(name, "tool"))
	lines := []string{outputStyle.Render(header)}
	for _, k := range sortedKeys(input) {
		lines = append(lines, outputStyle.Render(toolInputStyle.Render("    "+k+": "+formatToolInputValue(input[k]))))
	}
	return strings.Join(lines, "\n")
}

// renderToolCallActionsBlock formats a Codex commandExecution call using
// its parsed commandActions instead of dumping the raw shell wrapper.
//
// In short mode, each compacted action renders as its own top-level one-liner
// ("▸ read foo.go", "▸ search TODO in src/", "▸ git log -6"), matching the
// Codex explorer view. Full mode keeps a shell header for multi-action calls
// so the detail rows stay visually grouped.
func renderToolCallActionsBlock(name string, actions []map[string]any, mode toolOutputMode) string {
	rendered := compactCommandActions(actions)
	if len(rendered) == 0 {
		return ""
	}
	renderLine := func(a renderedCommandAction) string {
		text := a.title
		if a.body != "" {
			text += " " + a.body
		}
		return outputStyle.Render(diffPathStyle.Render("▸ " + text))
	}
	if len(rendered) == 1 {
		return renderLine(rendered[0])
	}
	if mode == toolOutputShort {
		lines := make([]string, 0, len(rendered))
		for _, a := range rendered {
			lines = append(lines, renderLine(a))
		}
		return strings.Join(lines, "\n")
	}
	header := diffPathStyle.Render("▸ " + nonEmpty(name, "tool"))
	lines := []string{outputStyle.Render(header)}
	for _, a := range rendered {
		row := "    " + a.title
		if a.body != "" {
			row += " " + a.body
		}
		lines = append(lines, outputStyle.Render(toolInputStyle.Render(row)))
	}
	return strings.Join(lines, "\n")
}

// renderedCommandAction is one display row produced from one or more
// CommandAction entries. title is the leading verb ("read", "search",
// "git", …) and body is the remainder ("foo.go, bar.go", "TODO in src/",
// "log -6"). They render as "title body" with a single space.
type renderedCommandAction struct {
	title string
	body  string
}

// compactCommandActions turns the raw commandActions slice into display
// rows. Consecutive Read actions fold into one row with deduplicated,
// comma-joined names so a "cat a.go b.go" call doesn't spam three lines.
// Other action types render one-to-one; Unknown extracts the program
// token as the title so "git log -6" shows as "git" + "log -6".
func compactCommandActions(actions []map[string]any) []renderedCommandAction {
	var out []renderedCommandAction
	i := 0
	for i < len(actions) {
		t, _ := actions[i]["type"].(string)
		if t == "read" {
			var names []string
			seen := map[string]bool{}
			j := i
			for j < len(actions) {
				nt, _ := actions[j]["type"].(string)
				if nt != "read" {
					break
				}
				name, _ := actions[j]["name"].(string)
				if name == "" {
					name, _ = actions[j]["path"].(string)
				}
				if name != "" && !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
				j++
			}
			out = append(out, renderedCommandAction{title: "read", body: strings.Join(names, ", ")})
			i = j
			continue
		}
		out = append(out, renderSingleCommandAction(actions[i]))
		i++
	}
	return out
}

// renderSingleCommandAction picks the title/body for one action entry.
// listFiles → "list <path>", search → "search <query> in <path>" (or
// fallbacks), unknown → "<program> <rest>" with the first token used as
// the verb so the user sees "git log" / "make build" instead of "run …".
// Unknown commands whose program is a known search tool (rg, fdfind, …)
// get reclassified as search since codex itself only tags `grep`.
func renderSingleCommandAction(a map[string]any) renderedCommandAction {
	t, _ := a["type"].(string)
	command, _ := a["command"].(string)
	switch t {
	case "read":
		name, _ := a["name"].(string)
		if name == "" {
			name, _ = a["path"].(string)
		}
		return renderedCommandAction{title: "read", body: name}
	case "listFiles":
		path, _ := a["path"].(string)
		if path == "" {
			path = command
		}
		return renderedCommandAction{title: "list", body: path}
	case "search":
		query, _ := a["query"].(string)
		path, _ := a["path"].(string)
		return renderedCommandAction{title: "search", body: searchBody(query, path, command)}
	case "unknown":
		prog, rest := splitProgramAndArgs(command)
		if isSearchProgram(prog) {
			query, path := parseSearchArgs(rest)
			return renderedCommandAction{title: "search", body: searchBody(query, path, command)}
		}
		return renderedCommandAction{title: prog, body: rest}
	}
	return renderedCommandAction{title: nonEmpty(t, "run"), body: command}
}

// summarizeShellCommand compresses a raw Bash command into the same kind
// of display row Codex uses for commandAction items. Shell wrappers are
// unwrapped first so `/usr/bin/zsh -lc 'git status'` renders as `git status`
// and common file/search commands become `read ...` / `search ...`.
func summarizeShellCommand(command string) string {
	if command == "" {
		return ""
	}
	script := unwrapShellWrapper(command)
	segments := splitShellSegments(script)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		tokens := splitShellTokens(segment)
		if len(tokens) == 0 {
			continue
		}
		switch tokens[0] {
		case "cd", "export", "source", "pwd", "true", "false":
			continue
		}
		row := summarizeCommandTokens(tokens)
		if row.title != "" {
			text := row.title
			if row.body != "" {
				text += " " + row.body
			}
			return text
		}
	}
	tokens := splitShellTokens(script)
	if len(tokens) == 0 {
		return truncate(script, 80)
	}
	row := summarizeCommandTokens(tokens)
	if row.title == "" {
		return truncate(script, 80)
	}
	text := row.title
	if row.body != "" {
		text += " " + row.body
	}
	return text
}

// summarizeCommandTokens maps a tokenized command to a compact display
// label. This intentionally tracks the same broad categories Codex uses
// for commandActions so the history row reads like the explorer view.
func summarizeCommandTokens(tokens []string) renderedCommandAction {
	prog := tokens[0]
	rest := strings.Join(tokens[1:], " ")
	switch prog {
	case "cat", "head", "tail", "less", "more":
		return renderedCommandAction{title: "read", body: strings.Join(splitPositionalArgs(tokens[1:]), ", ")}
	case "grep", "rg", "ag", "ack":
		query, path := parseSearchArgs(rest)
		return renderedCommandAction{title: "search", body: searchBody(query, path, rest)}
	case "ls", "fd", "fdfind":
		return renderedCommandAction{title: "list", body: joinPositionalTokens(tokens[1:])}
	}
	if rest == "" {
		return renderedCommandAction{title: prog}
	}
	return renderedCommandAction{title: prog, body: rest}
}

// unwrapShellWrapper strips the common `shell -lc 'script'` form so the
// summarizer can inspect the actual script instead of the shell launcher.
func unwrapShellWrapper(command string) string {
	tokens := splitShellTokens(strings.TrimSpace(command))
	if len(tokens) < 3 {
		return command
	}
	switch tokens[1] {
	case "-c", "-lc":
		switch base := tokens[0]; {
		case strings.HasSuffix(base, "bash"),
			strings.HasSuffix(base, "zsh"),
			strings.HasSuffix(base, "sh"),
			strings.HasSuffix(base, "fish"):
			return strings.Join(tokens[2:], " ")
		}
	}
	return command
}

// splitShellSegments breaks a shell-ish command into connector-delimited
// segments. It is intentionally small: enough to skip leading `cd` / env
// setup and pick the first meaningful command without trying to fully parse
// shell syntax.
func splitShellSegments(command string) []string {
	if command == "" {
		return nil
	}
	var segments []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		segments = append(segments, cur.String())
		cur.Reset()
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == 0 && c == '&' && i+1 < len(command) && command[i+1] == '&':
			flush()
			i++
		case quote == 0 && (c == ';' || c == '\n'):
			flush()
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
			cur.WriteByte(c)
		case quote != 0 && c == quote:
			quote = 0
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segments
}

// joinPositionalTokens keeps only the non-flag tokens and joins them back
// into a compact body string.
func joinPositionalTokens(tokens []string) string {
	return strings.Join(splitPositionalArgs(tokens), " ")
}

// splitPositionalArgs filters out flag-like tokens and returns the
// remaining positional arguments in order.
func splitPositionalArgs(tokens []string) []string {
	var positional []string
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		positional = append(positional, tok)
	}
	return positional
}

// searchPrograms is the set of CLIs we treat as text/file-search tools
// when codex hands us an "unknown" action. Codex itself recognizes
// `grep` and emits type:"search", but it currently misses ripgrep,
// fd-find, and a few others — we reclassify here so the row reads
// "search TODO in src/" instead of "rg TODO src/".
var searchPrograms = map[string]bool{
	"rg":     true,
	"fd":     true,
	"fdfind": true,
	"ag":     true,
	"ack":    true,
}

func isSearchProgram(name string) bool { return searchPrograms[name] }

// parseSearchArgs walks the post-program token stream, drops anything
// that looks like a flag, and returns the first two positional tokens
// as (query, path). Flags that take a separate value (`-t go`) will
// over-eat the next positional — we accept that for the common
// `<tool> PATTERN PATH` shape and don't try to encode per-tool flag
// arity. Quoted patterns are kept whole so `rg "foo bar" src/` parses
// to ("foo bar", "src/").
func parseSearchArgs(args string) (query, path string) {
	var positional []string
	for _, tok := range splitShellTokens(args) {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		positional = append(positional, tok)
	}
	if len(positional) > 0 {
		query = positional[0]
	}
	if len(positional) > 1 {
		path = positional[1]
	}
	return
}

// splitShellTokens splits on whitespace but treats matched single or
// double quotes as one token. Escapes and nested quoting aren't
// handled — codex's `command` strings are display-shaped, not
// roundtrippable shell, so the simple form is enough.
func splitShellTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == ' ' || c == '\t'):
			flush()
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return tokens
}

// searchBody formats a search action's body. With both query and path
// it reads "<query> in <path>"; with only a query, just the query;
// otherwise the raw command so the user still sees what ran.
func searchBody(query, path, command string) string {
	switch {
	case query != "" && path != "":
		return query + " in " + path
	case query != "":
		return query
	default:
		return command
	}
}

// splitProgramAndArgs splits a command string at its first whitespace so
// "git log --oneline -6" becomes ("git", "log --oneline -6"). An empty
// command falls back to "run" so the row never collapses to nothing.
func splitProgramAndArgs(cmd string) (string, string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "run", ""
	}
	idx := strings.IndexAny(cmd, " \t")
	if idx < 0 {
		return cmd, ""
	}
	return cmd[:idx], strings.TrimSpace(cmd[idx:])
}

// renderToolResultBlock formats the output of a tool call. Long output
// is clipped to toolOutputMaxLines / toolOutputMaxChars with a trailing
// "(… N more lines)" marker. Error results render with the error style
// so a failed command stands out against a pile of successful ones.
func renderToolResultBlock(output string, isError bool) string {
	body, trimmedLines := clampToolOutput(output)
	var rows []string
	for _, ln := range strings.Split(body, "\n") {
		styled := toolResultStyle.Render("    " + ln)
		if isError {
			styled = errStyle.Render("    " + ln)
		}
		rows = append(rows, outputStyle.Render(styled))
	}
	if trimmedLines > 0 {
		rows = append(rows, outputStyle.Render(toolResultStyle.Render(
			"    (… "+pluralLines(trimmedLines)+" omitted)")))
	}
	return strings.Join(rows, "\n")
}

func renderHookOutputBlock(eventName, output string, isError bool) string {
	eventName = nonEmpty(eventName, "hook")
	rows := []string{outputStyle.Render(diffPathStyle.Render("▸ " + eventName + " hook"))}
	body, trimmedLines := clampToolOutput(output)
	for _, ln := range strings.Split(body, "\n") {
		styled := toolResultStyle.Render("    " + ln)
		if isError {
			styled = errStyle.Render("    " + ln)
		}
		rows = append(rows, outputStyle.Render(styled))
	}
	if trimmedLines > 0 {
		rows = append(rows, outputStyle.Render(toolResultStyle.Render(
			"    (… "+pluralLines(trimmedLines)+" omitted)")))
	}
	return strings.Join(rows, "\n")
}

// clampToolOutput trims output to toolOutputMaxLines + toolOutputMaxChars.
// Returns the kept body plus the number of lines trimmed off so the
// caller can append a summary.
func clampToolOutput(s string) (string, int) {
	s = strings.TrimRight(s, "\n")
	if len(s) > toolOutputMaxChars {
		s = s[:toolOutputMaxChars]
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= toolOutputMaxLines {
		return s, 0
	}
	return strings.Join(lines[:toolOutputMaxLines], "\n"), len(lines) - toolOutputMaxLines
}

// formatToolInputValue stringifies one tool-input value. Short strings
// pass through verbatim; everything else becomes compact JSON so a
// reader can still see what was passed without drowning in pretty
// formatting.
func formatToolInputValue(v any) string {
	switch x := v.(type) {
	case string:
		return truncate(x, 200)
	case nil:
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return truncate(string(b), 200)
}

// sortedKeys returns the map keys in stable ("command" before "cwd")
// alphabetical order so successive renders of the same payload don't
// flicker.
func sortedKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// tiny n; manual insertion sort keeps us off "sort" imports already
	// used sparingly in this file.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func pluralLines(n int) string {
	if n == 1 {
		return "1 more line"
	}
	return itoa(n) + " more lines"
}

// itoa avoids pulling strconv just for plural rendering. n is always
// non-negative here.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// toolUseResultPayload returns the tool-result payload from either wire shape:
// live stream events use "tool_use_result" while persisted jsonl records use
// "toolUseResult".
func toolUseResultPayload(rec map[string]any) any {
	if v, ok := rec["tool_use_result"]; ok {
		return v
	}
	return rec["toolUseResult"]
}

// parseToolResultText extracts a human-readable output body from tool-result
// payloads while preserving whether the payload marked itself as an error.
func parseToolResultText(v any) (text string, isError bool, ok bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", false, false
		}
		return s, strings.HasPrefix(s, "Error:"), true
	case []any:
		var parts []string
		for _, item := range t {
			s, err, got := parseToolResultText(item)
			if !got {
				continue
			}
			parts = append(parts, s)
			isError = isError || err
		}
		if len(parts) == 0 {
			return "", false, false
		}
		return strings.Join(parts, "\n\n"), isError, true
	case map[string]any:
		isError, _ = t["is_error"].(bool)
		noOutputExpected, _ := t["noOutputExpected"].(bool)
		stdout, _ := t["stdout"].(string)
		stderr, _ := t["stderr"].(string)
		stdout = strings.TrimSpace(stdout)
		stderr = strings.TrimSpace(stderr)
		var parts []string
		if stdout != "" {
			parts = append(parts, stdout)
		}
		if stderr != "" {
			parts = append(parts, stderr)
			isError = true
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n"), isError, true
		}
		if noOutputExpected {
			return "", false, false
		}
		for _, k := range []string{"output", "content", "text", "message", "error"} {
			if raw, exists := t[k]; exists {
				s, err, got := parseToolResultText(raw)
				if !got {
					continue
				}
				return s, isError || err || k == "error", true
			}
		}
	}
	return "", false, false
}
