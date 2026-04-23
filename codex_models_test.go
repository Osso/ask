package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCodexParseModelList_PromotesDefaultThenRest(t *testing.T) {
	raw := []byte(`[
		{"id":"gpt-5-fast","model":"gpt-5","displayName":"GPT-5 Fast","hidden":false,"isDefault":false},
		{"id":"gpt-5","model":"gpt-5","displayName":"GPT-5","hidden":false,"isDefault":true},
		{"id":"hidden-model","model":"hm","displayName":"hidden","hidden":true,"isDefault":false},
		{"id":"o3","model":"o3","displayName":"O3","hidden":false,"isDefault":false}
	]`)
	var data []any
	_ = json.Unmarshal(raw, &data)
	got := codexParseModelList(data)
	want := []string{"gpt-5", "gpt-5-fast", "o3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("model ids=%v want %v", got, want)
	}
}

func TestCodexParseModelList_SkipsBadEntries(t *testing.T) {
	// Entries missing id, wrong shape, or hidden=true all drop out.
	raw := []byte(`[
		{"id":"","model":"","displayName":"empty"},
		"string-not-object",
		{"id":"good","model":"g","displayName":"good","hidden":false,"isDefault":false}
	]`)
	var data []any
	_ = json.Unmarshal(raw, &data)
	got := codexParseModelList(data)
	if len(got) != 1 || got[0] != "good" {
		t.Errorf("got %v want [good]", got)
	}
}

func TestCodex_ModelPicker_IncludesCachedModels(t *testing.T) {
	// With cached model list in config, the picker should surface
	// them alongside the "default" + "Enter your own" rows.
	isolateHome(t)
	cfg := askConfig{
		Codex: codexConfig{Models: []string{"gpt-5", "o3"}},
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	var cp codexProvider
	picker := cp.ModelPicker()
	// Expected: default, gpt-5, o3 (in that order). Quick switcher
	// appends "Enter your own"; /model uses AllowCustom branch.
	want := []string{"default", "gpt-5", "o3"}
	if !reflect.DeepEqual(picker.Options, want) {
		t.Errorf("Options=%v want %v", picker.Options, want)
	}
	if !picker.AllowCustom {
		t.Error("AllowCustom must stay true so typing still works")
	}
}

func TestCodex_ModelPicker_FallsBackToDefaultOnly(t *testing.T) {
	isolateHome(t)
	var cp codexProvider
	picker := cp.ModelPicker()
	if len(picker.Options) != 1 || picker.Options[0] != "default" {
		t.Errorf("uncached picker should be just [default], got %v", picker.Options)
	}
}
