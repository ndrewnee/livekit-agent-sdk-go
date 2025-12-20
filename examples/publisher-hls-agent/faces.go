package main

import "path/filepath"

const facesDirName = "faces"
const facesGroupsManifestName = "groups.json"

type faceExtractionSummary struct {
	Detected         int
	Saved            int
	SkippedDuplicate int
	SkippedSimilar   int
	SkippedLimit     int
	FilteredNonFace  int
	Groups           int
	Files            []string
}

func facesOutputDir(baseOutputDir string) string {
	return filepath.Join(baseOutputDir, facesDirName)
}
