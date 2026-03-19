package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"

	_ "image/gif"
	_ "image/jpeg"

	_ "golang.org/x/image/webp"

	"golang.org/x/image/draw"
)

// supportsGraphics checks whether the terminal supports the Kitty graphics protocol.
func supportsGraphics() bool {
	term := os.Getenv("TERM_PROGRAM")
	switch term {
	case "kitty", "WezTerm":
		return true
	}
	termName := os.Getenv("TERM")
	if strings.Contains(termName, "kitty") {
		return true
	}
	// iTerm2 uses its own inline image protocol but also supports Kitty's
	if term == "iTerm.app" {
		return true
	}
	return false
}

// renderImage loads and renders an image file as Kitty graphics escape sequences.
// maxWidth and maxHeight are in terminal cell units.
func renderImage(filePath string, maxWidth, maxHeight int) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return fallbackText(), nil
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return fallbackText(), nil
	}

	// Estimate pixel dimensions: assume ~8px per cell width, ~16px per cell height
	pxWidth := maxWidth * 8
	pxHeight := maxHeight * 16

	// Scale to fit within bounds while preserving aspect ratio
	srcBounds := src.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	scaleW := float64(pxWidth) / float64(srcW)
	scaleH := float64(pxHeight) / float64(srcH)
	scale := scaleW
	if scaleH < scale {
		scale = scaleH
	}
	if scale > 1 {
		scale = 1 // don't upscale
	}

	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return fallbackText(), nil
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	// Build Kitty graphics escape sequence with chunked transfer
	var sb strings.Builder
	const chunkSize = 4096

	for i := 0; i < len(encoded); i += chunkSize {
		end := i + chunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		chunk := encoded[i:end]
		more := 1
		if end == len(encoded) {
			more = 0
		}
		if i == 0 {
			sb.WriteString(fmt.Sprintf("\x1b_Ga=T,f=100,m=%d;%s\x1b\\", more, chunk))
		} else {
			sb.WriteString(fmt.Sprintf("\x1b_Gm=%d;%s\x1b\\", more, chunk))
		}
	}

	// Add blank lines to reserve space for the image in the viewport
	cellRows := dstH / 16
	if dstH%16 != 0 {
		cellRows++
	}
	if cellRows < 1 {
		cellRows = 1
	}
	for i := 0; i < cellRows; i++ {
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

func fallbackText() string {
	return dimStyle.Render("[Image preview unavailable — press o to open]")
}
