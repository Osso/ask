package main

import (
	"errors"
	"strings"
	"testing"
)

// withClipboardStubs swaps the package-level clipboard seams for the
// duration of the test so we never spawn a real subprocess. Callers
// pass the GOOS to simulate, the set of binaries that should "exist"
// on PATH, and a recorder that captures every successful write.
func withClipboardStubs(t *testing.T, goos string, present map[string]bool, run func(name, stdin string, args ...string) error) {
	t.Helper()
	prevGOOS, prevLook, prevRun := clipboardGOOS, clipboardLookPath, clipboardRun
	t.Cleanup(func() {
		clipboardGOOS, clipboardLookPath, clipboardRun = prevGOOS, prevLook, prevRun
	})
	clipboardGOOS = goos
	clipboardLookPath = func(name string) (string, error) {
		if present[name] {
			return "/fake/" + name, nil
		}
		return "", errors.New("not found")
	}
	clipboardRun = run
}

func TestClipboardCopyText_DarwinUsesPbcopy(t *testing.T) {
	var ranName, ranStdin string
	var ranArgs []string
	withClipboardStubs(t, "darwin",
		map[string]bool{"pbcopy": true},
		func(name, stdin string, args ...string) error {
			ranName, ranStdin, ranArgs = name, stdin, args
			return nil
		})
	if err := clipboardCopyText("hello mac"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ranName != "pbcopy" {
		t.Errorf("ran %q, want pbcopy", ranName)
	}
	if ranStdin != "hello mac" {
		t.Errorf("stdin %q, want hello mac", ranStdin)
	}
	if len(ranArgs) != 0 {
		t.Errorf("pbcopy got args %v, want none", ranArgs)
	}
}

func TestClipboardCopyText_LinuxPrefersWlCopy(t *testing.T) {
	var ranName string
	withClipboardStubs(t, "linux",
		map[string]bool{"wl-copy": true, "xclip": true, "xsel": true},
		func(name, stdin string, args ...string) error {
			ranName = name
			return nil
		})
	if err := clipboardCopyText("hi"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ranName != "wl-copy" {
		t.Errorf("ran %q, want wl-copy (highest priority on linux)", ranName)
	}
}

func TestClipboardCopyText_LinuxFallsBackToXclip(t *testing.T) {
	var ranName string
	var ranArgs []string
	withClipboardStubs(t, "linux",
		map[string]bool{"xclip": true},
		func(name, stdin string, args ...string) error {
			ranName, ranArgs = name, args
			return nil
		})
	if err := clipboardCopyText("hi"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ranName != "xclip" {
		t.Errorf("ran %q, want xclip fallback", ranName)
	}
	if got := strings.Join(ranArgs, " "); got != "-selection clipboard" {
		t.Errorf("xclip args=%q, want -selection clipboard", got)
	}
}

func TestClipboardCopyText_NoBinaryAvailable(t *testing.T) {
	withClipboardStubs(t, "linux",
		map[string]bool{},
		func(name, stdin string, args ...string) error {
			t.Fatalf("clipboardRun should not be called when no binary present")
			return nil
		})
	err := clipboardCopyText("hi")
	if err == nil {
		t.Fatal("expected error when no clipboard binary is available")
	}
	if !strings.Contains(err.Error(), "wl-copy") {
		t.Errorf("error %q should list the writers tried", err)
	}
}

func TestClipboardCopyText_UnsupportedGOOS(t *testing.T) {
	withClipboardStubs(t, "plan9",
		map[string]bool{},
		func(name, stdin string, args ...string) error {
			t.Fatalf("clipboardRun should not be called on unsupported OS")
			return nil
		})
	err := clipboardCopyText("hi")
	if err == nil || !strings.Contains(err.Error(), "plan9") {
		t.Fatalf("expected unsupported-OS error mentioning plan9, got %v", err)
	}
}

func TestClipboardCopyText_PropagatesRunError(t *testing.T) {
	withClipboardStubs(t, "darwin",
		map[string]bool{"pbcopy": true},
		func(name, stdin string, args ...string) error {
			return errors.New("boom")
		})
	err := clipboardCopyText("hi")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected pbcopy run error to propagate, got %v", err)
	}
}
