package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Benchmarks for TTS parallel generation performance
// Run with: go test -bench=. ./internal/media

func BenchmarkTTSParallelGeneration(b *testing.B) {
	// Setup mock server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate API latency
		time.Sleep(100 * time.Millisecond)

		// Return mock opus data
		mockData := make([]byte, 1024) // 1KB mock audio
		w.Header().Set("Content-Type", "audio/opus")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockData)
	}))
	defer mockServer.Close()

	// Create TTS stage
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})
	stage.SetEndpoint(mockServer.URL)

	ctx := context.Background()

	testCases := []struct {
		name      string
		languages int
	}{
		{"2_languages", 2},
		{"5_languages", 5},
		{"10_languages", 10},
		{"20_languages", 20},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			// Create translations map
			translations := make(map[string]string)
			for i := 0; i < tc.languages; i++ {
				translations[string(rune('a'+i))] = "Test text for TTS generation"
			}

			input := MediaData{
				Type:    MediaTypeAudio,
				TrackID: "bench-track",
				Metadata: map[string]interface{}{
					"transcription_event": TranscriptionEvent{
						Text:         "Original text",
						Language:     "en",
						Translations: translations,
					},
				},
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := stage.Process(ctx, input)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTTSSingleGeneration(b *testing.B) {
	// Setup mock server with minimal latency
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockData := make([]byte, 512) // 512 bytes mock audio
		w.Header().Set("Content-Type", "audio/opus")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockData)
	}))
	defer mockServer.Close()

	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})
	stage.SetEndpoint(mockServer.URL)
	stage.SetRateLimiter(nil) // Disable rate limiting for benchmarks
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := stage.generateTTS(ctx, "Benchmark text for TTS generation")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTTSCircuitBreaker(b *testing.B) {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stage.canMakeAPICall()
	}
}

func BenchmarkTTSRateLimit(b *testing.B) {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stage.rateLimiter.Allow()
	}
}

func BenchmarkTTSMetricsUpdate(b *testing.B) {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stage.updateStats(5, 4, 250*time.Millisecond)
	}
}

// Memory allocation benchmarks
func BenchmarkTTSMemoryAllocation(b *testing.B) {
	// Setup mock server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockData := make([]byte, 256)
		w.Header().Set("Content-Type", "audio/opus")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockData)
	}))
	defer mockServer.Close()

	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})
	stage.SetEndpoint(mockServer.URL)
	stage.SetRateLimiter(nil) // Disable rate limiting for benchmarks
	ctx := context.Background()

	translations := map[string]string{
		"es": "Texto de prueba",
		"fr": "Texte de test",
		"de": "Testtext",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// This will test memory allocation in parallel processing
		_ = stage.generateTTSParallel(ctx, translations)
	}
}

// Concurrent load test
func BenchmarkTTSConcurrentLoad(b *testing.B) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Simulate API latency
		mockData := make([]byte, 256)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockData)
	}))
	defer mockServer.Close()

	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "bench",
		Priority: 50,
		APIKey:   "test-key",
	})
	stage.SetEndpoint(mockServer.URL)
	stage.SetRateLimiter(nil) // Disable rate limiting for benchmarks
	ctx := context.Background()

	translations := map[string]string{
		"es": "Concurrent test",
		"fr": "Test concurrent",
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "concurrent-bench",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Translations: translations,
			},
		},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := stage.Process(ctx, input)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
