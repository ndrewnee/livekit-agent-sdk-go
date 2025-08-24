package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
)

// VideoGeneratorService generates video frames
type VideoGeneratorService struct {
	config VideoConfig
}

// NewVideoGeneratorService creates a new video generator service
func NewVideoGeneratorService(config VideoConfig) VideoGeneratorService {
	return VideoGeneratorService{config: config}
}

// GenerateFrame generates a video frame based on the pattern type
func (s VideoGeneratorService) GenerateFrame(patternType string, width, height int, frameNumber uint32) []byte {
	// Create RGBA image
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Generate pattern
	switch patternType {
	case "color_bars":
		s.drawColorBars(img, int(frameNumber))
	case "moving_circle":
		s.drawMovingCircle(img, int(frameNumber))
	case "checkerboard":
		s.drawCheckerboard(img, int(frameNumber))
	case "gradient":
		s.drawGradient(img, int(frameNumber))
	default:
		s.drawColorBars(img, int(frameNumber))
	}

	// Convert to YUV420 format for VP8 encoding
	return s.convertToYUV420(img)
}

// drawColorBars draws standard color bars pattern
func (s VideoGeneratorService) drawColorBars(img *image.RGBA, frameNumber int) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	colors := []color.RGBA{
		{255, 255, 255, 255}, // White
		{255, 255, 0, 255},   // Yellow
		{0, 255, 255, 255},   // Cyan
		{0, 255, 0, 255},     // Green
		{255, 0, 255, 255},   // Magenta
		{255, 0, 0, 255},     // Red
		{0, 0, 255, 255},     // Blue
		{0, 0, 0, 255},       // Black
	}

	barWidth := width / len(colors)

	for i := range colors {
		x0 := i * barWidth
		x1 := (i + 1) * barWidth
		if i == len(colors)-1 {
			x1 = width // Ensure last bar fills remaining width
		}

		// Optional: Add animation by shifting colors
		colorIndex := (i + frameNumber/30) % len(colors)
		currentColor := colors[colorIndex]

		// Draw vertical bar
		for y := 0; y < height; y++ {
			for x := x0; x < x1; x++ {
				img.Set(x, y, currentColor)
			}
		}
	}

	// Add frame counter overlay
	s.drawFrameCounter(img, frameNumber)
}

// drawMovingCircle draws a moving circle pattern
func (s VideoGeneratorService) drawMovingCircle(img *image.RGBA, frameNumber int) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Fill background with dark gray
	draw.Draw(img, bounds, &image.Uniform{color.RGBA{40, 40, 40, 255}}, image.Point{}, draw.Src)

	// Calculate circle position using sine/cosine for smooth motion
	centerX := width/2 + int(float64(width/3)*s.sin(float64(frameNumber)*0.02))
	centerY := height/2 + int(float64(height/3)*s.cos(float64(frameNumber)*0.03))
	radius := 60

	// Draw filled circle with changing color
	circleColor := color.RGBA{
		uint8(128 + 127*s.sin(float64(frameNumber)*0.05)),
		uint8(128 + 127*s.sin(float64(frameNumber)*0.07+2)),
		uint8(128 + 127*s.sin(float64(frameNumber)*0.09+4)),
		255,
	}

	// Draw circle using simple algorithm
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx := x - centerX
			dy := y - centerY
			distSq := dx*dx + dy*dy
			radiusSq := radius * radius

			if distSq <= radiusSq {
				// Anti-aliasing at edges
				if distSq > (radius-2)*(radius-2) {
					// Blend with background
					bg := img.RGBAAt(x, y)
					alpha := 1.0 - float64(distSq-((radius-2)*(radius-2)))/float64(4*radius)
					blended := s.blendColors(circleColor, bg, alpha)
					img.Set(x, y, blended)
				} else {
					img.Set(x, y, circleColor)
				}
			}
		}
	}

	// Add frame counter overlay
	s.drawFrameCounter(img, frameNumber)
}

// drawCheckerboard draws a checkerboard pattern
func (s VideoGeneratorService) drawCheckerboard(img *image.RGBA, frameNumber int) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	squareSize := 50
	offset := frameNumber % (squareSize * 2) // Animate by shifting pattern

	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Calculate which square we're in
			squareX := (x + offset) / squareSize
			squareY := y / squareSize

			// Checkerboard logic
			if (squareX+squareY)%2 == 0 {
				img.Set(x, y, white)
			} else {
				img.Set(x, y, black)
			}
		}
	}

	// Add colored border
	borderColor := color.RGBA{
		uint8(128 + 127*s.sin(float64(frameNumber)*0.03)),
		uint8(128 + 127*s.sin(float64(frameNumber)*0.04)),
		uint8(128 + 127*s.sin(float64(frameNumber)*0.05)),
		255,
	}
	s.drawBorder(img, 10, borderColor)

	// Add frame counter overlay
	s.drawFrameCounter(img, frameNumber)
}

// drawGradient draws an animated gradient pattern
func (s VideoGeneratorService) drawGradient(img *image.RGBA, frameNumber int) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Create animated gradient
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Calculate gradient values with animation
			r := uint8((float64(x)/float64(width) + s.sin(float64(frameNumber)*0.01)) * 255)
			g := uint8((float64(y)/float64(height) + s.sin(float64(frameNumber)*0.02)) * 255)
			b := uint8((s.sin(float64(x+y+frameNumber)*0.005) + 1) * 127)

			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}

	// Add frame counter overlay
	s.drawFrameCounter(img, frameNumber)
}

// drawFrameCounter adds a frame counter overlay
func (s VideoGeneratorService) drawFrameCounter(img *image.RGBA, frameNumber int) {
	// Draw a semi-transparent background for the counter
	counterBg := color.RGBA{0, 0, 0, 180}
	for y := 10; y < 40; y++ {
		for x := 10; x < 150; x++ {
			img.Set(x, y, counterBg)
		}
	}

	// In a real implementation, you would render text here
	// For now, we'll just draw a simple indicator
	indicatorColor := color.RGBA{255, 255, 255, 255}
	indicatorX := 20 + (frameNumber % 100)
	for y := 20; y < 30; y++ {
		img.Set(indicatorX, y, indicatorColor)
		img.Set(indicatorX+1, y, indicatorColor)
	}
}

// drawBorder draws a border around the image
func (s VideoGeneratorService) drawBorder(img *image.RGBA, thickness int, c color.RGBA) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Top and bottom borders
	for y := 0; y < thickness; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, c)          // Top
			img.Set(x, height-1-y, c) // Bottom
		}
	}

	// Left and right borders
	for x := 0; x < thickness; x++ {
		for y := thickness; y < height-thickness; y++ {
			img.Set(x, y, c)         // Left
			img.Set(width-1-x, y, c) // Right
		}
	}
}

// convertToYUV420 converts RGBA image to YUV420 format
func (s VideoGeneratorService) convertToYUV420(img *image.RGBA) []byte {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// YUV420 has Y plane (width*height) + U plane (width*height/4) + V plane (width*height/4)
	yuvSize := width*height + width*height/2
	yuv := make([]byte, yuvSize)

	yOffset := 0
	uOffset := width * height
	vOffset := uOffset + width*height/4

	// Convert each pixel
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// Shift down to 8-bit
			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)

			// Convert to YUV using standard coefficients
			yVal := uint8((19595*int(r8) + 38470*int(g8) + 7471*int(b8) + 32768) >> 16)
			yuv[yOffset] = yVal
			yOffset++

			// Subsample U and V (every 2x2 block)
			if x%2 == 0 && y%2 == 0 {
				uVal := uint8(((-11056)*int(r8) + (-21712)*int(g8) + 32768*int(b8) + 8421376) >> 16)
				vVal := uint8((32768*int(r8) + (-27440)*int(g8) + (-5328)*int(b8) + 8421376) >> 16)

				uIdx := uOffset + (y/2)*(width/2) + x/2
				vIdx := vOffset + (y/2)*(width/2) + x/2

				if uIdx < len(yuv) && vIdx < len(yuv) {
					yuv[uIdx] = uVal
					yuv[vIdx] = vVal
				}
			}
		}
	}

	// In a real implementation, this would be properly encoded as VP8
	// For now, we return the raw YUV data
	return yuv
}

// Helper functions

func (s VideoGeneratorService) sin(x float64) float64 {
	// Simple sine approximation to avoid importing math
	x = s.fmod(x, 2*3.14159265359)
	if x < 0 {
		x += 2 * 3.14159265359
	}

	// Taylor series approximation
	x3 := x * x * x
	x5 := x3 * x * x
	x7 := x5 * x * x
	return x - x3/6 + x5/120 - x7/5040
}

func (s VideoGeneratorService) cos(x float64) float64 {
	return s.sin(x + 3.14159265359/2)
}

func (s VideoGeneratorService) fmod(x, y float64) float64 {
	return x - float64(int(x/y))*y
}

func (s VideoGeneratorService) blendColors(fg, bg color.RGBA, alpha float64) color.RGBA {
	return color.RGBA{
		R: uint8(float64(fg.R)*alpha + float64(bg.R)*(1-alpha)),
		G: uint8(float64(fg.G)*alpha + float64(bg.G)*(1-alpha)),
		B: uint8(float64(fg.B)*alpha + float64(bg.B)*(1-alpha)),
		A: 255,
	}
}

// CreateIVFHeader creates a simple IVF header for VP8
func (s VideoGeneratorService) CreateIVFHeader() []byte {
	header := &bytes.Buffer{}
	header.Write([]byte("DKIF")) // Signature
	header.Write([]byte{0, 0})   // Version
	header.Write([]byte{32, 0})  // Header length
	header.Write([]byte("VP80")) // Codec FourCC
	header.Write(s.uint16ToBytes(uint16(s.config.Width)))
	header.Write(s.uint16ToBytes(uint16(s.config.Height)))
	header.Write(s.uint32ToBytes(uint32(s.config.FrameRate)))
	header.Write(s.uint32ToBytes(1)) // Time base denominator
	header.Write(s.uint32ToBytes(0)) // Number of frames (0 for stream)
	header.Write([]byte{0, 0, 0, 0}) // Unused

	return header.Bytes()
}

func (s VideoGeneratorService) uint16ToBytes(v uint16) []byte {
	return []byte{byte(v), byte(v >> 8)}
}

func (s VideoGeneratorService) uint32ToBytes(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
