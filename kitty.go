package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"strings"
)

const pendingImageID uint32 = 1

func isKitty() bool {
	term := os.Getenv("TERM")
	if strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") {
		return true
	}
	return os.Getenv("KITTY_WINDOW_ID") != ""
}

func kittyTransmitPNG(id uint32, pngData []byte) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()

	b64 := base64.StdEncoding.EncodeToString(pngData)
	const chunk = 4096
	first := true
	for len(b64) > 0 {
		n := len(b64)
		more := 0
		if n > chunk {
			n = chunk
			more = 1
		}
		var header string
		if first {
			header = fmt.Sprintf("a=T,f=100,i=%d,U=1,q=2,m=%d", id, more)
			first = false
		} else {
			header = fmt.Sprintf("m=%d,q=2", more)
		}
		if _, err := fmt.Fprintf(tty, "\x1b_G%s;%s\x1b\\", header, b64[:n]); err != nil {
			return err
		}
		b64 = b64[n:]
	}
	return nil
}

func kittyPlaceholderRows(id uint32, cols, rows int) string {
	if rows <= 0 || cols <= 0 {
		return ""
	}
	if rows > len(kittyDiacritics) {
		rows = len(kittyDiacritics)
	}
	if cols > len(kittyDiacritics) {
		cols = len(kittyDiacritics)
	}
	r := byte(id >> 16)
	g := byte(id >> 8)
	b := byte(id)
	colorOn := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	colorOff := "\x1b[39m"

	var out strings.Builder
	for ri := 0; ri < rows; ri++ {
		out.WriteString(colorOn)
		out.WriteRune(0x10EEEE)
		out.WriteRune(kittyDiacritics[ri])
		out.WriteRune(kittyDiacritics[0])
		for ci := 1; ci < cols; ci++ {
			out.WriteRune(0x10EEEE)
		}
		out.WriteString(colorOff)
		if ri < rows-1 {
			out.WriteRune('\n')
		}
	}
	return out.String()
}

func thumbnailGrid(imgW, imgH int) (cols, rows int) {
	const maxCols = 20
	const maxRows = 8
	const cellW, cellH = 10, 20
	if imgW <= 0 || imgH <= 0 {
		return 4, 2
	}
	boxW := maxCols * cellW
	boxH := maxRows * cellH
	scaleW := float64(boxW) / float64(imgW)
	scaleH := float64(boxH) / float64(imgH)
	scale := scaleW
	if scaleH < scale {
		scale = scaleH
	}
	pxW := float64(imgW) * scale
	pxH := float64(imgH) * scale
	cols = int((pxW + float64(cellW-1)) / float64(cellW))
	rows = int((pxH + float64(cellH-1)) / float64(cellH))
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	if cols > maxCols {
		cols = maxCols
	}
	if rows > maxRows {
		rows = maxRows
	}
	return
}

func encodeToPNG(data []byte) ([]byte, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}
