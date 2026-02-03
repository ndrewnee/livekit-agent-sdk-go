package agent

import (
	"os"
	"testing"
)

func requireOpenAI(tb testing.TB) string {
	tb.Helper()

	// Explicit opt-in to avoid accidental real API usage (cost, flakiness, network access).
	if os.Getenv("RUN_OPENAI_TESTS") != "true" {
		tb.Skip("Skipping real OpenAI test (set RUN_OPENAI_TESTS=true to enable)")
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		tb.Skip("Skipping real OpenAI test: OPENAI_API_KEY not set")
	}

	return apiKey
}
