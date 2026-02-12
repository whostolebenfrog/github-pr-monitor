package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

var (
	// Cache icons to avoid regenerating
	iconNormal []byte
	iconAlert  []byte
)

func init() {
	iconNormal = generateIconWithAlert(false)
	iconAlert = generateIconWithAlert(true)
}

// getIcon returns the appropriate icon based on whether there are PRs needing attention
func getIcon(hasAlerts bool) []byte {
	if hasAlerts {
		return iconAlert
	}
	return iconNormal
}

// generateIconWithAlert creates a PR icon for the menu bar
// Uses white color for visibility on dark menu bars
// Adds a red notification dot when hasAlert is true
func generateIconWithAlert(hasAlert bool) []byte {
	const size = 22
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Use white for the icon (visible on dark menu bars)
	white := color.RGBA{255, 255, 255, 255}

	// Draw a simple PR/merge icon:
	// - A vertical line on the left (source branch)
	// - A diagonal line merging into a vertical line on the right (target branch)

	// Left vertical line (source branch) - from top to middle
	for y := 3; y <= 11; y++ {
		img.Set(6, y, white)
		img.Set(7, y, white)
	}

	// Right vertical line (target branch) - full height
	for y := 3; y <= 18; y++ {
		img.Set(14, y, white)
		img.Set(15, y, white)
	}

	// Diagonal merge line from left branch to right branch
	for i := 0; i <= 7; i++ {
		x := 7 + i
		y := 11 + (i * 3 / 7)
		img.Set(x, y, white)
		img.Set(x, y+1, white)
	}

	// Small circle at top of left branch (commit dot)
	drawCircle(img, 6, 4, 2, white)

	// Small circle at top of right branch (commit dot)
	drawCircle(img, 14, 4, 2, white)

	// Small circle at bottom of right branch (merge point)
	drawCircle(img, 14, 17, 2, white)

	// Add red notification dot in top-right corner if there are alerts
	if hasAlert {
		red := color.RGBA{255, 59, 48, 255} // iOS-style red
		drawCircle(img, 15, 6, 8, red)
	}

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
