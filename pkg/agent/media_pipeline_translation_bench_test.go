package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// BenchmarkTranslationCacheHit benchmarks cache hit performance.
func BenchmarkTranslationCacheHit(b *testing.B) {
	stage := NewTranslationStage("bench", 30, "test-key", "")
	defer stage.Disconnect()

	// Pre-populate cache
	text := "Benchmark test text"
	targetLangs := []string{"es", "fr"}
	cacheKey := stage.generateCacheKey(text, "en", targetLangs)
	translations := map[string]string{"es": "Texto de prueba", "fr": "Texte de test"}
	stage.cacheTranslation(cacheKey, translations)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = stage.getCachedTranslation(cacheKey)
		}
	})
}

// BenchmarkTranslationProcessing benchmarks translation processing performance.
func BenchmarkTranslationProcessing(b *testing.B) {
	stage := NewTranslationStage("bench", 30, "test-key", "")
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
	stage := NewTranslationStage("bench", 30, "test-key", "")
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
