package main

import (
	"io"
	"os"
)

const (
	mouseAlternateScrollEnable  = "\x1b[?1007h"
	mouseAlternateScrollDisable = "\x1b[?1007l"
)

func writeMouseAlternateScrollMode(w io.Writer, enabled bool) error {
	seq := mouseAlternateScrollDisable
	if enabled {
		seq = mouseAlternateScrollEnable
	}
	_, err := io.WriteString(w, seq)
	return err
}

func setMouseAlternateScroll(enabled bool) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	return writeMouseAlternateScrollMode(tty, enabled)
}
