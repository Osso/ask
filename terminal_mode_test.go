package main

import (
	"bytes"
	"testing"
)

func TestWriteMouseAlternateScrollMode(t *testing.T) {
	var b bytes.Buffer
	if err := writeMouseAlternateScrollMode(&b, false); err != nil {
		t.Fatalf("disable err=%v", err)
	}
	if got, want := b.String(), "\x1b[?1007l"; got != want {
		t.Fatalf("disable sequence=%q want %q", got, want)
	}

	b.Reset()
	if err := writeMouseAlternateScrollMode(&b, true); err != nil {
		t.Fatalf("enable err=%v", err)
	}
	if got, want := b.String(), "\x1b[?1007h"; got != want {
		t.Fatalf("enable sequence=%q want %q", got, want)
	}
}
