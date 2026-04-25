package main

import "testing"

func TestAyuTheme_UsesAccentForegroundHighlight(t *testing.T) {
	ayu := ayuTheme()

	if got := hexOf(ayu.highlightFG); got != hexOf(ayu.accent) {
		t.Fatalf("ayu highlight foreground = %s, want accent %s", got, hexOf(ayu.accent))
	}

	style := buildGlamourStyle(ayu)
	if got := *style.Code.StylePrimitive.Color; got != "#E6B450" {
		t.Fatalf("ayu inline code foreground = %s, want accent #E6B450", got)
	}
	if style.Code.StylePrimitive.BackgroundColor != nil {
		t.Fatalf("ayu inline code background = %s, want no forced background", *style.Code.StylePrimitive.BackgroundColor)
	}
	if got := *style.CodeBlock.Chroma.LiteralString.Color; got != "#95E6CB" {
		t.Fatalf("ayu string foreground = %s, want softer cyan #95E6CB", got)
	}
	if style.CodeBlock.Chroma.LiteralString.BackgroundColor != nil {
		t.Fatalf("ayu string background = %s, want no token background", *style.CodeBlock.Chroma.LiteralString.BackgroundColor)
	}
}
