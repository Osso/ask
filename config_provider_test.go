package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// configProviderFixture seeds the registry with two fake providers and
// returns a test model currently on the first one, in modeConfig with
// the provider picker ready to open.
func configProviderFixture(t *testing.T) model {
	t.Helper()
	isolateHome(t)
	p1 := newFakeProvider()
	p1.id = "claude"
	p1.displayName = "Claude"
	p2 := newFakeProvider()
	p2.id = "codex"
	p2.displayName = "Codex"
	withRegisteredProviders(t, p1, p2)
	m := newTestModel(t, p1)
	m = m.startConfigModal()
	return m
}

func TestConfigItems_IncludesProviderRow(t *testing.T) {
	m := configProviderFixture(t)
	var found bool
	for _, it := range m.configItemsAll() {
		if it.id == "provider" {
			found = true
			if it.key != "Claude" {
				t.Errorf("provider row key=%q want Claude (current provider)", it.key)
			}
			break
		}
	}
	if !found {
		t.Fatal("configItemsAll must include a 'provider' row")
	}
}

func TestOpenConfigProviderPicker_CursorsOnCurrent(t *testing.T) {
	m := configProviderFixture(t)
	m = m.openConfigProviderPicker()
	if !m.configProviderPickerActive {
		t.Error("picker should be active after open")
	}
	if m.configProviderBackup != "claude" {
		t.Errorf("backup=%q want claude", m.configProviderBackup)
	}
	if m.configProviderCursor != 0 {
		t.Errorf("cursor=%d want 0 (claude idx)", m.configProviderCursor)
	}
}

func TestConfigProviderPicker_EscRestoresWithoutSaving(t *testing.T) {
	m := configProviderFixture(t)
	m = m.openConfigProviderPicker()

	mi, _ := m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mi.(model)
	mi, _ = m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mi.(model)

	if m.configProviderPickerActive {
		t.Error("picker should close on Esc")
	}
	cfg, _ := loadConfig()
	if cfg.Provider != "" {
		t.Errorf("cfg.Provider=%q; Esc must not save", cfg.Provider)
	}
}

func TestConfigProviderPicker_EnterSavesDefault(t *testing.T) {
	m := configProviderFixture(t)
	m = m.openConfigProviderPicker()

	// Move to codex (idx 1), Enter to save.
	mi, _ := m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mi.(model)
	mi, _ = m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mi.(model)

	if m.configProviderPickerActive {
		t.Error("picker should close on Enter")
	}
	cfg, _ := loadConfig()
	if cfg.Provider != "codex" {
		t.Errorf("cfg.Provider=%q want codex after save", cfg.Provider)
	}
}

func TestConfigProviderPicker_DoesNotSwitchCurrentTab(t *testing.T) {
	// Picking a new default in /config must NOT swap the current tab's
	// provider. Only new tabs pick up cfg.Provider on newTab.
	m := configProviderFixture(t)
	preID := m.provider.ID()
	m = m.openConfigProviderPicker()
	mi, _ := m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyDown})
	m = mi.(model)
	mi, _ = m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mi.(model)
	if m.provider == nil || m.provider.ID() != preID {
		got := "<nil>"
		if m.provider != nil {
			got = m.provider.ID()
		}
		t.Errorf("current tab provider changed from %q to %q — /config should only set default", preID, got)
	}
}

func TestConfigProviderPicker_EnterReturnsToInputMode(t *testing.T) {
	m := configProviderFixture(t)
	m = m.openConfigProviderPicker()
	mi, _ := m.updateConfigProviderPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mi.(model)
	if m.mode != modeInput {
		t.Errorf("mode after save=%v want modeInput (picker closed + config modal cleared)", m.mode)
	}
}
