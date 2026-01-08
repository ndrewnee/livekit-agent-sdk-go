//go:build !gocv

package main

import (
	"context"
	"fmt"
)

func extractAndSaveUniqueFacesFromImages(_ context.Context, _ []string, _ string, _ faceExtractionConfig) (faceExtractionSummary, error) {
	return faceExtractionSummary{}, fmt.Errorf("face extraction requires building with -tags gocv")
}
