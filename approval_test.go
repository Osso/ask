package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// approvalStateModel returns a model in modeApproval ready for keystroke
// tests, with a buffered reply channel so sendApproval doesn't block.
func approvalStateModel(t *testing.T, tool string, input map[string]any) (model, chan approvalReply) {
	t.Helper()
	m := newTestModel(t, newFakeProvider())
	reply := make(chan approvalReply, 1)
	m = m.startApproval(approvalRequestMsg{toolName: tool, input: input, reply: reply})
	return m, reply
}

func feedRunes(m model, s string) model {
	for _, r := range s {
		nm, _ := m.updateApproval(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = nm.(model)
	}
	return m
}

func TestApproval_StartResetsFeedbackState(t *testing.T) {
	m, _ := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	if m.approvalFeedback != "" {
		t.Errorf("approvalFeedback=%q want empty", m.approvalFeedback)
	}
	if m.approvalFeedbackFocus {
		t.Error("approvalFeedbackFocus should start false")
	}
	if m.approvalChoice != approvalChoiceDeny {
		t.Errorf("approvalChoice=%d want deny(0)", m.approvalChoice)
	}
}

func TestApproval_TabCyclesButtonsThenFocusesFeedback(t *testing.T) {
	m, _ := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})

	// Tab: deny → allow
	nm, _ := m.updateApproval(tea.KeyPressMsg{Code: tea.KeyTab})
	m = nm.(model)
	if m.approvalChoice != approvalChoiceAllow || m.approvalFeedbackFocus {
		t.Fatalf("step1: choice=%d focus=%v want choice=allow focus=false", m.approvalChoice, m.approvalFeedbackFocus)
	}

	// Tab: allow → always
	nm, _ = m.updateApproval(tea.KeyPressMsg{Code: tea.KeyTab})
	m = nm.(model)
	if m.approvalChoice != approvalChoiceAlways || m.approvalFeedbackFocus {
		t.Fatalf("step2: choice=%d focus=%v want choice=always focus=false", m.approvalChoice, m.approvalFeedbackFocus)
	}

	// Tab: always → feedback (focus shifts; choice stays on always so the
	// rendered button still reflects the action that will fire on Enter)
	nm, _ = m.updateApproval(tea.KeyPressMsg{Code: tea.KeyTab})
	m = nm.(model)
	if !m.approvalFeedbackFocus {
		t.Fatalf("step3: expected focus on feedback, got choice=%d focus=%v", m.approvalChoice, m.approvalFeedbackFocus)
	}
	if m.approvalChoice != approvalChoiceAlways {
		t.Errorf("step3: choice should remain on always, got %d", m.approvalChoice)
	}

	// Tab: feedback → deny (focus returns to buttons)
	nm, _ = m.updateApproval(tea.KeyPressMsg{Code: tea.KeyTab})
	m = nm.(model)
	if m.approvalFeedbackFocus {
		t.Fatalf("step4: expected focus back on buttons, got focus=true")
	}
	if m.approvalChoice != approvalChoiceDeny {
		t.Errorf("step4: choice=%d want deny", m.approvalChoice)
	}
}

func TestApproval_TextInputAppendsToFeedbackWhenFocused(t *testing.T) {
	m, _ := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m.approvalFeedbackFocus = true
	m.approvalChoice = approvalChoiceAlways

	m = feedRunes(m, "git status")
	if m.approvalFeedback != "git status" {
		t.Errorf("approvalFeedback=%q want %q", m.approvalFeedback, "git status")
	}
}

func TestApproval_TextInputIgnoredWhenButtonsFocused(t *testing.T) {
	m, _ := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	// Buttons-mode 'g' has no shortcut binding; verify it does NOT leak
	// into approvalFeedback.
	nm, _ := m.updateApproval(tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = nm.(model)
	if m.approvalFeedback != "" {
		t.Errorf("buttons-mode keystroke leaked into feedback: %q", m.approvalFeedback)
	}
}

func TestApproval_BackspaceTrimsFeedback(t *testing.T) {
	m, _ := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m.approvalFeedbackFocus = true
	m.approvalFeedback = "git statusx"

	nm, _ := m.updateApproval(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = nm.(model)
	if m.approvalFeedback != "git status" {
		t.Errorf("backspace: %q want %q", m.approvalFeedback, "git status")
	}
}

func TestApproval_FeedbackMaxLenEnforced(t *testing.T) {
	m, _ := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m.approvalFeedbackFocus = true

	// Pre-fill to the cap.
	big := make([]byte, approvalFeedbackMaxLen)
	for i := range big {
		big[i] = 'a'
	}
	m.approvalFeedback = string(big)

	// One more keystroke should bounce off.
	nm, _ := m.updateApproval(tea.KeyPressMsg{Code: 'b', Text: "b"})
	m = nm.(model)
	if len(m.approvalFeedback) != approvalFeedbackMaxLen {
		t.Errorf("len=%d want cap %d", len(m.approvalFeedback), approvalFeedbackMaxLen)
	}
}

func TestApproval_SendAlwaysWithEmptyFeedbackUsesAddRulesPath(t *testing.T) {
	m, reply := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m = m.sendApproval(approvalChoiceAlways)
	r := <-reply
	if !r.allow {
		t.Error("expected allow=true")
	}
	if r.feedback != "" {
		t.Errorf("feedback=%q want empty", r.feedback)
	}
	if r.remember == nil {
		t.Fatal("remember should be set on always-without-feedback")
	}
	if r.remember.toolName != "Bash" || r.remember.ruleContent != "ls" {
		t.Errorf("remember=%+v want Bash/ls", r.remember)
	}
	if m.mode != modeInput {
		t.Errorf("mode=%v want modeInput", m.mode)
	}
}

func TestApproval_SendAlwaysWithFeedbackTakesPersistRulePath(t *testing.T) {
	m, reply := approvalStateModel(t, "Bash", map[string]any{"command": "git status"})
	m.approvalFeedback = "let me run git status variants"
	m = m.sendApproval(approvalChoiceAlways)
	r := <-reply
	if !r.allow {
		t.Error("expected allow=true")
	}
	if r.remember != nil {
		t.Errorf("remember=%+v want nil for feedback path", r.remember)
	}
	if r.feedback != "let me run git status variants" {
		t.Errorf("feedback=%q want full text", r.feedback)
	}
}

func TestApproval_FeedbackOnlyAppliesToAlwaysChoice(t *testing.T) {
	m, reply := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m.approvalFeedback = "type some feedback"
	// User submits with choice=allow (one-shot allow), feedback should NOT
	// trigger the persist-rule path.
	m = m.sendApproval(approvalChoiceAllow)
	r := <-reply
	if !r.allow {
		t.Error("expected allow=true")
	}
	if r.feedback != "" {
		t.Errorf("feedback=%q must be empty unless choice=always", r.feedback)
	}
	if r.remember != nil {
		t.Errorf("remember=%+v want nil for one-shot allow", r.remember)
	}
}

func TestApproval_DenyClearsFeedback(t *testing.T) {
	m, reply := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m.approvalFeedback = "ignored"
	m.approvalFeedbackFocus = true

	m = m.sendApproval(approvalChoiceDeny)
	r := <-reply
	if r.allow {
		t.Error("expected allow=false on deny")
	}
	if r.feedback != "" || r.remember != nil {
		t.Errorf("deny carried state: feedback=%q remember=%+v", r.feedback, r.remember)
	}
	if m.approvalFeedback != "" || m.approvalFeedbackFocus {
		t.Error("modal state should reset after deny")
	}
}

func TestApproval_EnterFromFeedbackSubmitsCurrentChoice(t *testing.T) {
	m, reply := approvalStateModel(t, "Bash", map[string]any{"command": "git status"})
	// Walk through Tab cycle to land on feedback with choice=always.
	for i := 0; i < 3; i++ {
		nm, _ := m.updateApproval(tea.KeyPressMsg{Code: tea.KeyTab})
		m = nm.(model)
	}
	if !m.approvalFeedbackFocus || m.approvalChoice != approvalChoiceAlways {
		t.Fatalf("setup: expected focus=true choice=always, got focus=%v choice=%d", m.approvalFeedbackFocus, m.approvalChoice)
	}
	m = feedRunes(m, "trust git read-only")

	// Enter: submit.
	nm, _ := m.updateApproval(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = nm.(model)
	r := <-reply
	if !r.allow || r.feedback != "trust git read-only" || r.remember != nil {
		t.Errorf("reply=%+v want allow=true feedback set remember=nil", r)
	}
	if m.mode != modeInput {
		t.Errorf("mode=%v want modeInput post-submit", m.mode)
	}
}

func TestApproval_EscFromFeedbackDenies(t *testing.T) {
	m, reply := approvalStateModel(t, "Bash", map[string]any{"command": "ls"})
	m.approvalFeedbackFocus = true

	nm, _ := m.updateApproval(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = nm.(model)
	r := <-reply
	if r.allow {
		t.Error("esc must deny")
	}
}
