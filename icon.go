package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// generateIcon creates a simple PR icon for the menu bar
// It's a 22x22 template image (black on transparent) showing a merge/PR symbol
func generateIcon() []byte {
	const size = 22
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Draw a simple PR/merge icon:
	// - A vertical line on the left (source branch)
	// - A diagonal line merging into a vertical line on the right (target branch)
	black := color.RGBA{0, 0, 0, 255}

	// Left vertical line (source branch) - from top to middle
	for y := 3; y <= 11; y++ {
		img.Set(6, y, black)
		img.Set(7, y, black)
	}

	// Right vertical line (target branch) - full height
	for y := 3; y <= 18; y++ {
		img.Set(14, y, black)
		img.Set(15, y, black)
	}

	// Diagonal merge line from left branch to right branch
	// Goes from (7, 11) to (14, 14)
	for i := 0; i <= 7; i++ {
		x := 7 + i
		y := 11 + (i * 3 / 7)
		img.Set(x, y, black)
		img.Set(x, y+1, black)
	}

	// Small circle at top of left branch (commit dot)
	drawCircle(img, 6, 4, 2, black)

	// Small circle at top of right branch (commit dot)
	drawCircle(img, 14, 4, 2, black)

	// Small circle at bottom of right branch (merge point)
	drawCircle(img, 14, 17, 2, black)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

func drawCircle(img *image.RGBA, cx, cy, r int, c color.Color) {
	for x := cx - r; x <= cx+r; x++ {
		for y := cy - r; y <= cy+r; y++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= r*r {
				img.Set(x, y, c)
			}
		}
	}
}
