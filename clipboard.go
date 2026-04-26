package main

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// clipboardLookPath and clipboardRun are package-level seams so tests can
// stub the binary-selection / write logic without spawning real subprocesses.
// Production code uses exec.LookPath and a real *exec.Cmd write.
var (
	clipboardLookPath = exec.LookPath
	clipboardRun      = func(name string, stdin string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.Run()
	}
	clipboardGOOS = runtime.GOOS
)

// clipboardWriter pairs a binary name with the args it needs. Picked at
// runtime by clipboardCopyText based on GOOS and PATH availability.
type clipboardWriter struct {
	name string
	args []string
}

// clipboardWritersFor returns the writer candidates to try, in order, for
// the given GOOS. macOS gets pbcopy; Linux tries the Wayland writer first
// then the X11 fallbacks; everything else is empty (caller surfaces the
// no-binary error).
func clipboardWritersFor(goos string) []clipboardWriter {
	switch goos {
	case "darwin":
		return []clipboardWriter{{name: "pbcopy"}}
	case "linux":
		return []clipboardWriter{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	default:
		return nil
	}
}

// clipboardCopyText writes s to the OS clipboard. Picks pbcopy on macOS
// and wl-copy / xclip / xsel on Linux (in that order). Returns a
// descriptive error when no compatible binary is on PATH so the caller
// can surface it via toast.
func clipboardCopyText(s string) error {
	writers := clipboardWritersFor(clipboardGOOS)
	if len(writers) == 0 {
		return fmt.Errorf("clipboard not supported on %s", clipboardGOOS)
	}
	var tried []string
	for _, w := range writers {
		if _, err := clipboardLookPath(w.name); err != nil {
			tried = append(tried, w.name)
			continue
		}
		if err := clipboardRun(w.name, s, w.args...); err != nil {
			return fmt.Errorf("%s: %w", w.name, err)
		}
		return nil
	}
	return fmt.Errorf("no clipboard binary available (tried %s)", strings.Join(tried, ", "))
}

type imagePastedMsg struct {
	data       []byte
	mime       string
	pngForKitty []byte
	width      int
	height     int
	err        error
}

var acceptedImageMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

func pasteImageCmd() tea.Cmd {
	return func() tea.Msg {
		listOut, err := exec.Command("wl-paste", "--list-types").Output()
		if err != nil {
			return imagePastedMsg{err: errors.New("wl-paste failed (clipboard empty or wl-paste missing)")}
		}
		var mime string
		for _, t := range strings.Split(string(listOut), "\n") {
			t = strings.TrimSpace(t)
			if acceptedImageMimes[t] {
				mime = t
				break
			}
		}
		if mime == "" {
			return imagePastedMsg{err: errors.New("no image in clipboard")}
		}
		data, err := exec.Command("wl-paste", "--type", mime, "--no-newline").Output()
		if err != nil {
			return imagePastedMsg{err: err}
		}
		if len(data) == 0 {
			return imagePastedMsg{err: errors.New("clipboard image was empty")}
		}
		msg := imagePastedMsg{data: data, mime: mime}
		if png, w, h, derr := encodeToPNG(data); derr == nil {
			msg.pngForKitty = png
			msg.width = w
			msg.height = h
		}
		return msg
	}
}
