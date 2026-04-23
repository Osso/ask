package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCodexInterrupt_SendsTurnInterruptWithIDs(t *testing.T) {
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{
		stdin:         buf,
		threadID:      "tid",
		currentTurnID: "turn-7",
		nextID:        10,
	}
	p := &providerProc{stdin: buf, payload: state}

	var cp codexProvider
	handled, err := cp.Interrupt(p)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if !handled {
		t.Errorf("handled=false with threadID+turnID in state; want true")
	}
	var env map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &env); err != nil {
		t.Fatalf("invalid JSON %q: %v", buf.String(), err)
	}
	if env["method"] != "turn/interrupt" {
		t.Errorf("method=%v want turn/interrupt", env["method"])
	}
	params, _ := env["params"].(map[string]any)
	if params["threadId"] != "tid" {
		t.Errorf("threadId=%v", params["threadId"])
	}
	if params["turnId"] != "turn-7" {
		t.Errorf("turnId=%v want turn-7", params["turnId"])
	}
}

func TestCodexInterrupt_FallsBackWhenNoTurnID(t *testing.T) {
	// No turn in flight (turnID empty) → handled=false so the
	// caller falls back to killProc rather than writing a malformed
	// turn/interrupt.
	buf := &bufferCloser{Buffer: &bytes.Buffer{}}
	state := &codexState{stdin: buf, threadID: "tid"}
	p := &providerProc{stdin: buf, payload: state}
	var cp codexProvider
	handled, err := cp.Interrupt(p)
	if err != nil || handled {
		t.Errorf("Interrupt without turnID should be (false,nil), got (%v,%v)", handled, err)
	}
	if buf.Len() != 0 {
		t.Errorf("no frames should be written when interrupt is a no-op, got %q", buf.String())
	}
}

func TestCodexInterrupt_FallsBackWhenNilPayload(t *testing.T) {
	p := &providerProc{}
	var cp codexProvider
	handled, err := cp.Interrupt(p)
	if err != nil || handled {
		t.Errorf("Interrupt with nil state must fallback; got (%v,%v)", handled, err)
	}
}

func TestClaudeInterrupt_AlwaysFallsBack(t *testing.T) {
	var cp claudeProvider
	handled, err := cp.Interrupt(&providerProc{})
	if err != nil {
		t.Errorf("claude.Interrupt: %v", err)
	}
	if handled {
		t.Errorf("claude has no cancel protocol; handled must be false")
	}
}

func TestModel_CancelTurn_UsesInterruptWhenProviderHandles(t *testing.T) {
	// When the provider returns handled=true, cancelTurn should NOT
	// kill the proc; the UI should show a "cancelling…" state and
	// wait for turn/completed.
	fp := newFakeProvider()
	interruptCalls := 0
	fp.interruptFn = func(p *providerProc) (bool, error) {
		interruptCalls++
		return true, nil
	}
	m := newTestModel(t, fp)
	m.busy = true
	m.proc = &providerProc{stderr: &stderrBuf{}}

	after, _ := m.cancelTurn()
	if interruptCalls != 1 {
		t.Errorf("Interrupt should have been called once, got %d", interruptCalls)
	}
	if after.proc == nil {
		t.Errorf("cancelTurn must not kill proc when interrupt succeeds")
	}
	if after.status != "cancelling…" {
		t.Errorf("status=%q want 'cancelling…'", after.status)
	}
}

func TestModel_CancelTurn_FallsBackWhenProviderRefuses(t *testing.T) {
	fp := newFakeProvider()
	fp.interruptFn = func(p *providerProc) (bool, error) { return false, nil }
	m := newTestModel(t, fp)
	m.busy = true
	m.proc = &providerProc{stderr: &stderrBuf{}}

	after, _ := m.cancelTurn()
	if after.proc != nil {
		t.Errorf("handled=false must trigger killProc; proc still alive")
	}
}

func TestModel_CancelTurn_CooperativeReturnsWatchdogCmd(t *testing.T) {
	// The cooperative cancel path must return a tea.Cmd so the
	// runtime schedules the watchdog; without it there'd be no
	// fallback if the provider never winds the turn down.
	fp := newFakeProvider()
	fp.interruptFn = func(p *providerProc) (bool, error) { return true, nil }
	m := newTestModel(t, fp)
	m.busy = true
	m.proc = &providerProc{stderr: &stderrBuf{}}

	_, cmd := m.cancelTurn()
	if cmd == nil {
		t.Error("cooperative cancel must arm a cancelWatchdogCmd")
	}
}

func TestUpdate_CancelWatchdog_KillsWhenStillBusy(t *testing.T) {
	// When the watchdog fires and the same proc is still live and
	// busy, the fallback kicks in: killProc and append a notice.
	fp := newFakeProvider()
	m := newTestModel(t, fp)
	m.busy = true
	m.proc = &providerProc{stderr: &stderrBuf{}}

	mm, _ := m.Update(cancelWatchdogMsg{proc: m.proc})
	out := mm.(model)
	if out.proc != nil {
		t.Errorf("watchdog must killProc; proc still alive")
	}
}

func TestUpdate_CancelWatchdog_NoOpWhenStaleProc(t *testing.T) {
	// Stale watchdog for a proc that's already been replaced: drop
	// the message so we don't accidentally clobber a fresh session.
	fp := newFakeProvider()
	m := newTestModel(t, fp)
	m.busy = true
	m.proc = &providerProc{stderr: &stderrBuf{}}
	stale := &providerProc{}

	mm, _ := m.Update(cancelWatchdogMsg{proc: stale})
	out := mm.(model)
	if out.proc != m.proc {
		t.Error("stale watchdog must not touch the live proc")
	}
}
