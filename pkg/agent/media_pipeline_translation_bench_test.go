package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// BenchmarkTranslationProcessing benchmarks translation processing performance.
func BenchmarkTranslationProcessing(b *testing.B) {
	// Setup mock OpenAI streaming server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set headers for Server-Sent Events
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		// Send mock streaming response
		mockResponse := `data: {"choices":[{"delta":{"content":"{\"es\":\"Texto de prueba de benchmark\"}"}}]}

data: [DONE]

`
		_, _ = w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	stage := NewTranslationStage(&TranslationConfig{
		Name:     "bench",
		Priority: 30,
		APIKey:   "test-key",
	})
	stage.SetEndpoint(mockServer.URL)
	defer stage.Disconnect()

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		var counter int64
		for pb.Next() {
			input := MediaData{
				Type:    MediaTypeAudio,
				TrackID: fmt.Sprintf("bench_track_%d", atomic.AddInt64(&counter, 1)),
				Metadata: map[string]interface{}{
					"transcription_event": TranscriptionEvent{
						Text:     "Benchmark test text",
						Language: "en",
						IsFinal:  true,
					},
					"target_languages": []string{"es"},
				},
			}
			_, _ = stage.Process(ctx, input)
		}
	})
}

// BenchmarkLanguageFiltering benchmarks language filtering logic.
func BenchmarkLanguageFiltering(b *testing.B) {
	stage := NewTranslationStage(&TranslationConfig{
		Name:     "bench",
		Priority: 30,
		APIKey:   "test-key",
	})
	defer stage.Disconnect()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		metaTargetLangs := []string{"en", "es", "fr", "de", "it", "pt"}
		var counter int64
		for pb.Next() {
			sourceLang := "en"
			// Inline filtering logic (same as in Process method)
			targetLangs := make([]string, 0, len(metaTargetLangs))
			for _, lang := range metaTargetLangs {
				if lang != sourceLang && lang != "" {
					targetLangs = append(targetLangs, lang)
				}
			}
			_ = targetLangs // Use result to avoid optimization
			atomic.AddInt64(&counter, 1)
		}
	})
}
