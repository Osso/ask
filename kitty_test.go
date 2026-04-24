package main

import (
	"image"
	"image/color"
	"testing"
)

func TestCenterCropAndScaleUsesImageCenter(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	src.Set(0, 0, color.NRGBA{R: 255, A: 255})
	src.Set(1, 0, color.NRGBA{G: 255, A: 255})
	src.Set(2, 0, color.NRGBA{B: 255, A: 255})
	src.Set(3, 0, color.NRGBA{R: 255, G: 255, A: 255})

	got := centerCropAndScale(src, 2, 1)

	if left := color.NRGBAModel.Convert(got.At(0, 0)).(color.NRGBA); left != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("left pixel = %#v, want centered green crop", left)
	}
	if right := color.NRGBAModel.Convert(got.At(1, 0)).(color.NRGBA); right != (color.NRGBA{B: 255, A: 255}) {
		t.Fatalf("right pixel = %#v, want centered blue crop", right)
	}
}
