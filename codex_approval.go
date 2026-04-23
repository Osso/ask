package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Codex sends approval requests as server-to-client JSON-RPC requests
// — frames with both `id` and `method`. The UI handles them through
// the same approval modal claude uses (`approvalRequestMsg` +
// `approvalReply`); the translation between our allow/deny tri-state
// and codex's ReviewDecision enum happens inside the per-request
// responder goroutine below, which writes the result back on the same
// request id that arrived.

// codexApprovalMethods names the codex server-request methods we
// convert into approval modals. Anything else routes through
// handleCodexUnknownRequest so codex doesn't block waiting on us.
var codexApprovalMethods = map[string]bool{
	"execCommandApproval":      true,
	"applyPatchApproval":       true,
	"fileChangeRequestApproval": true,
}

// codexServerRequest checks whether ev is a server-side JSON-RPC
// request (has both id and method). Returns the id and method when so.
func codexServerRequest(ev map[string]any) (id any, method string, ok bool) {
	rawID, hasID := ev["id"]
	if !hasID {
		return nil, "", false
	}
	method, _ = ev["method"].(string)
	if method == "" {
		return nil, "", false
	}
	return rawID, method, true
}

// handleCodexServerRequest is called from readCodexStream on every
// frame that's a server-initiated request. Known approval kinds emit
// an approvalRequestMsg and spawn a responder; unknown ones get a
// JSON-RPC error reply so codex can move on. Returns the messages (if
// any) to forward to the tea runtime.
func handleCodexServerRequest(proc *providerProc, ev map[string]any) ([]tea.Msg, bool) {
	id, method, ok := codexServerRequest(ev)
	if !ok {
		return nil, false
	}
	params, _ := ev["params"].(map[string]any)
	state, _ := proc.payload.(*codexState)

	if !codexApprovalMethods[method] {
		// Unknown / unimplemented server request: reply with Method
		// Not Found so codex doesn't hang on a client that doesn't
		// speak this RPC.
		_ = codexWriteJSONLocked(state, codexErrorResponse(id, -32601,
			"ask: method not handled: "+method))
		debugLog("codex unknown server-request id=%v method=%q params-keys=%v",
			id, method, mapKeys(params))
		return nil, false
	}

	// Skip-all-permissions belt-and-suspenders: even though we pass
	// approvalPolicy=never in thread/start params, answer "approved"
	// without bothering the user if for any reason a request still
	// fires.
	if state != nil && state.skipPerms {
		_ = codexWriteJSONLocked(state, codexResponse(id, map[string]any{
			"decision": "approved",
		}))
		debugLog("codex auto-approved (skipPerms) id=%v method=%q", id, method)
		return nil, true
	}

	toolName, input := codexApprovalSummary(method, params)
	reply := make(chan approvalReply, 1)
	go respondCodexApproval(state, id, reply)
	return []tea.Msg{approvalRequestMsg{
		tabID:    state.tabID,
		toolName: toolName,
		input:    input,
		reply:    reply,
	}}, true
}

// respondCodexApproval listens for the UI's decision (or a proc-dead
// signal) and writes the JSON-RPC response back on codex's stdin.
func respondCodexApproval(state *codexState, id any, reply chan approvalReply) {
	if state == nil {
		return
	}
	var r approvalReply
	select {
	case r = <-reply:
	case <-state.done:
		// Proc already exited — nothing to reply to.
		return
	}
	decision := "denied"
	switch {
	case r.allow && r.remember != nil:
		// The "always allow" UI option maps to codex's
		// approved_for_session so follow-up requests in the same
		// thread slot skip the modal.
		decision = "approved_for_session"
	case r.allow:
		decision = "approved"
	}
	if err := codexWriteJSONLocked(state, codexResponse(id, map[string]any{
		"decision": decision,
	})); err != nil {
		debugLog("codex approval response write failed id=%v: %v", id, err)
	}
}

// codexApprovalSummary extracts a display-friendly tool name and input
// map from a codex approval params blob. Reuses the existing approval
// modal renderer, which already knows how to summarise Bash commands
// and file paths — we just need to translate codex's shapes into its
// expected keys (`command` for exec, `file_path` for file changes).
func codexApprovalSummary(method string, params map[string]any) (string, map[string]any) {
	switch method {
	case "execCommandApproval":
		cmdParts, _ := params["command"].([]any)
		cmd := codexJoinCommand(cmdParts)
		input := map[string]any{"command": cmd}
		if reason, _ := params["reason"].(string); reason != "" {
			input["reason"] = reason
		}
		if cwd, _ := params["cwd"].(string); cwd != "" {
			input["cwd"] = cwd
		}
		return "Bash", input
	case "applyPatchApproval":
		files, _ := params["fileChanges"].(map[string]any)
		paths := codexCollectPaths(files)
		input := map[string]any{
			"file_path": strings.Join(paths, ", "),
			"files":     len(paths),
		}
		if reason, _ := params["reason"].(string); reason != "" {
			input["reason"] = reason
		}
		return "ApplyPatch", input
	case "fileChangeRequestApproval":
		input := map[string]any{}
		if itemID, _ := params["itemId"].(string); itemID != "" {
			input["item_id"] = itemID
		}
		if reason, _ := params["reason"].(string); reason != "" {
			input["reason"] = reason
		}
		if grant, _ := params["grantRoot"].(string); grant != "" {
			input["file_path"] = grant
		}
		return "FileChange", input
	}
	return method, params
}

// codexJoinCommand turns a []any of strings into a shell-ish single
// line, matching how the existing Bash approval summary renders.
func codexJoinCommand(parts []any) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s, ok := p.(string); ok {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

// codexCollectPaths returns the keys of a fileChanges map sorted in
// a stable-enough order for the modal summary. No sort needed — the
// modal truncates anyway and order is informational.
func codexCollectPaths(files map[string]any) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	return out
}

// codexResponse builds a JSON-RPC 2.0 success response for the given
// request id.
func codexResponse(id any, result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
}

// codexErrorResponse builds a JSON-RPC 2.0 error response.
func codexErrorResponse(id any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
}

// mapKeysString is an unused-sentinel to silence `go vet` in the rare
// case nothing in this file references strings — keeps the import
// footprint explicit without triggering unused-import errors when
// debugLog is a no-op in release builds. (debugLog itself uses fmt.)
var _ = fmt.Sprintf
