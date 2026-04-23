package main

import (
	tea "charm.land/bubbletea/v2"
)

// sessionArgs bundles the model's current state into the provider-shaped
// arg struct used by Provider.StartSession / ProbeInit.
func (m model) sessionArgs() ProviderSessionArgs {
	return ProviderSessionArgs{
		Cwd:                m.cwd,
		MCPPort:            m.mcpPort,
		Model:              m.providerModel,
		Effort:             m.providerEffort,
		OllamaHost:         m.ollamaHost,
		OllamaModel:        m.ollamaModel,
		SkipAllPermissions: m.skipAllPermissions,
		Worktree:           m.worktree,
		SessionID:          m.sessionID,
		ResumeCwd:          m.resumeCwd,
	}
}

// ensureProc lazily starts a provider session on first send in a turn.
// Subsequent calls are no-ops while the process is alive.
//
// For providers without a native --worktree flag, ensureProc manages
// the worktree lifecycle itself when the user's worktree preference is
// on: a `.claude/worktrees/<name>` sibling is created before fork, the
// subprocess runs inside it (via ProviderSessionArgs.Cwd), and the
// existing prune/lock scaffolding reaps it on shutdown. Providers that
// advertise NativeWorktree=true still drive their own worktree code
// path and emit providerCwdMsg so the chip updates asynchronously.
func (m *model) ensureProc() error {
	if m.proc != nil {
		return nil
	}
	args := m.sessionArgs()
	if !m.provider.Capabilities().NativeWorktree {
		switch {
		case m.worktree && m.sessionID == "" && inGitCheckout():
			path, name, err := createExternalWorktree()
			if err != nil {
				return err
			}
			args.Cwd = path
			m.worktreeName = name
		case m.sessionID != "" && m.resumeCwd != "":
			m.worktreeName = worktreeNameFromCwd(m.resumeCwd)
		}
	}
	proc, ch, err := m.provider.StartSession(args)
	if err != nil {
		return err
	}
	m.proc = proc
	m.streamCh = ch
	return nil
}

// sendToProvider delivers a user turn (text + pending attachments) to
// the active provider session, starting a fresh subprocess if needed.
// Returns the right tea.Cmd composition for spinner ticks and stream
// readers.
func (m model) sendToProvider(line string) (tea.Model, tea.Cmd) {
	nAtt := len(m.pending)
	debugLog("sendToProvider provider=%s line=%q attachments=%d procNil=%v busy=%v sessionID=%q",
		m.provider.ID(), line, nAtt, m.proc == nil, m.busy, m.sessionID)
	m.appendUser(userBarText(line, nAtt))
	newProc := m.proc == nil
	wasIdle := !m.busy
	if err := m.ensureProc(); err != nil {
		debugLog("ensureProc err: %v", err)
		m.appendHistory(outputStyle.Render(errStyle.Render("could not start " + m.provider.DisplayName() + ": " + err.Error())))
		return m, nil
	}
	if err := m.provider.Send(m.proc, line, m.pending); err != nil {
		debugLog("provider send err: %v", err)
		m.appendHistory(outputStyle.Render(errStyle.Render("write to " + m.provider.DisplayName() + " failed: " + err.Error())))
		m.killProc()
		return m, nil
	}
	m.pending = nil
	m.busy = true
	m.status = "thinking…"
	var cmds []tea.Cmd
	if newProc {
		cmds = append(cmds, nextStreamCmd(m.streamCh))
	}
	if wasIdle {
		cmds = append(cmds, m.spinner.Tick)
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// cancelTurn kills the active provider process and reports to the user.
func (m model) cancelTurn() model {
	if !m.busy && m.proc == nil {
		return m
	}
	m.flushTurnBuffer()
	m.killProc()
	m.appendHistory(outputStyle.Render(dimStyle.Render("✗ cancelled")))
	return m
}

func (m *model) flushTurnBuffer() {
	if len(m.turnBuffer) == 0 {
		return
	}
	last := m.turnBuffer[len(m.turnBuffer)-1]
	m.turnBuffer = nil
	m.appendResponse(last)
}

// killProc tears down the active provider subprocess (if any) and
// resets all per-turn UI state. Safe to call on an idle model.
func (m *model) killProc() {
	if m.proc == nil {
		return
	}
	m.proc.kill()
	m.proc = nil
	m.streamCh = nil
	m.busy = false
	m.status = ""
	m.todos = nil
	m.bgTasks = nil
}

// drainPendingReplies unblocks any MCP tool call that was waiting on
// this tab (ask/approval modal). Called when a tab is closed with a
// modal still open so the provider's request side doesn't hang on a
// dangling channel.
func (m *model) drainPendingReplies() {
	if m.askReply != nil {
		select {
		case m.askReply <- askReply{cancelled: true}:
		default:
		}
		m.askReply = nil
	}
	if m.approvalReply != nil {
		select {
		case m.approvalReply <- approvalReply{allow: false}:
		default:
		}
		m.approvalReply = nil
	}
}
