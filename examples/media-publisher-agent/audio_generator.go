package main

import (
	"math"
)

// AudioGeneratorService generates audio samples
type AudioGeneratorService struct {
	config AudioConfig
}

// NewAudioGeneratorService creates a new audio generator service
func NewAudioGeneratorService(config AudioConfig) AudioGeneratorService {
	return AudioGeneratorService{config: config}
}

// GenerateTone generates a sine wave tone at the specified frequency
func (s AudioGeneratorService) GenerateTone(frequency float64, duration float64) []byte {
	sampleRate := s.config.SampleRate
	channels := s.config.Channels
	samples := int(duration * float64(sampleRate))

	// Create buffer for 16-bit PCM samples
	audioData := make([]byte, samples*channels*2)

	// Generate sine wave
	for i := 0; i < samples; i++ {
		t := float64(i) / float64(sampleRate)
		amplitude := 0.3 * float64(0x7FFF) // 30% of max amplitude to avoid clipping
		sample := int16(amplitude * math.Sin(2*math.Pi*frequency*t))

		// For each channel
		for ch := 0; ch < channels; ch++ {
			idx := (i*channels + ch) * 2
			// Convert to bytes (little endian)
			audioData[idx] = byte(sample & 0xFF)
			audioData[idx+1] = byte((sample >> 8) & 0xFF)
		}
	}

	return audioData
}

// GenerateChord generates a chord (multiple frequencies combined)
func (s AudioGeneratorService) GenerateChord(frequencies []float64, duration float64) []byte {
	sampleRate := s.config.SampleRate
	channels := s.config.Channels
	samples := int(duration * float64(sampleRate))

	// Create buffer for 16-bit PCM samples
	audioData := make([]byte, samples*channels*2)

	// Reduce amplitude based on number of frequencies to avoid clipping
	amplitude := 0.3 * float64(0x7FFF) / float64(len(frequencies))

	// Generate combined sine waves
	for i := 0; i < samples; i++ {
		t := float64(i) / float64(sampleRate)
		var combinedSample float64

		// Sum all frequencies
		for _, freq := range frequencies {
			combinedSample += math.Sin(2 * math.Pi * freq * t)
		}

		sample := int16(amplitude * combinedSample)

		// For each channel
		for ch := 0; ch < channels; ch++ {
			idx := (i*channels + ch) * 2
			// Convert to bytes (little endian)
			audioData[idx] = byte(sample & 0xFF)
			audioData[idx+1] = byte((sample >> 8) & 0xFF)
		}
	}

	return audioData
}

// GenerateSweep generates a frequency sweep from startFreq to endFreq
func (s AudioGeneratorService) GenerateSweep(startFreq, endFreq, duration float64) []byte {
	sampleRate := s.config.SampleRate
	channels := s.config.Channels
	samples := int(duration * float64(sampleRate))

	// Create buffer for 16-bit PCM samples
	audioData := make([]byte, samples*channels*2)

	amplitude := 0.3 * float64(0x7FFF)

	// Generate frequency sweep
	for i := 0; i < samples; i++ {
		t := float64(i) / float64(sampleRate)
		progress := t / duration

		// Linear interpolation of frequency
		currentFreq := startFreq + (endFreq-startFreq)*progress

		sample := int16(amplitude * math.Sin(2*math.Pi*currentFreq*t))

		// For each channel
		for ch := 0; ch < channels; ch++ {
			idx := (i*channels + ch) * 2
			// Convert to bytes (little endian)
			audioData[idx] = byte(sample & 0xFF)
			audioData[idx+1] = byte((sample >> 8) & 0xFF)
		}
	}

	return audioData
}

// GenerateWhiteNoise generates white noise
func (s AudioGeneratorService) GenerateWhiteNoise(duration float64, amplitude float64) []byte {
	sampleRate := s.config.SampleRate
	channels := s.config.Channels
	samples := int(duration * float64(sampleRate))

	// Create buffer for 16-bit PCM samples
	audioData := make([]byte, samples*channels*2)

	// Simple linear congruential generator for pseudo-random numbers
	seed := uint32(42)
	maxAmplitude := amplitude * float64(0x7FFF)

	for i := 0; i < samples; i++ {
		// Generate pseudo-random number
		seed = seed*1664525 + 1013904223
		normalizedRandom := (float64(seed) / float64(0xFFFFFFFF)) - 0.5
		sample := int16(normalizedRandom * 2 * maxAmplitude)

		// For each channel
		for ch := 0; ch < channels; ch++ {
			idx := (i*channels + ch) * 2
			// Convert to bytes (little endian)
			audioData[idx] = byte(sample & 0xFF)
			audioData[idx+1] = byte((sample >> 8) & 0xFF)
		}
	}

	return audioData
}

// GenerateSilence generates silent audio
func (s AudioGeneratorService) GenerateSilence(duration float64) []byte {
	sampleRate := s.config.SampleRate
	channels := s.config.Channels
	samples := int(duration * float64(sampleRate))

	// Create buffer filled with zeros
	return make([]byte, samples*channels*2)
}

// GenerateToneSample generates a single sample for a tone at a specific time
func (s AudioGeneratorService) GenerateToneSample(frequency float64, time float64, volume float32) int16 {
	amplitude := volume * 0.3 * float32(0x7FFF) // 30% of max amplitude to avoid clipping
	sample := int16(amplitude * float32(math.Sin(2*math.Pi*frequency*time)))
	return sample
}
