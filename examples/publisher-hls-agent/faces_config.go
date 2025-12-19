package main

import (
	"fmt"
	"strings"
)

type faceExtractionConfig struct {
	Enabled             bool
	Detector            string
	CascadePath         string
	NormalizedWidth     int
	NormalizedHeight    int
	MaxPerThumbnail     int
	MaxUnique           int
	UniquenessThreshold int
	PaddingRatio        float64
	MinSize             int
	ScaleFactor         float64
	MinNeighbors        int
	Ext                 string

	YunetModelPath       string
	YunetScoreThreshold  float32
	YunetNMSThreshold    float32
	YunetTopK            int
	SFaceModelPath       string
	RecognitionThreshold float32
	WriteGroupsJSON      bool
}

func faceExtractionConfigFromConfig(cfg *Config) (faceExtractionConfig, error) {
	if cfg == nil {
		return faceExtractionConfig{}, fmt.Errorf("config is nil")
	}

	if !cfg.FacesEnabled {
		return faceExtractionConfig{Enabled: false}, nil
	}

	if cfg.FaceNormalizedWidth <= 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_NORMALIZED_WIDTH must be > 0")
	}
	if cfg.FaceNormalizedHeight <= 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_NORMALIZED_HEIGHT must be > 0")
	}
	if cfg.FaceMaxPerThumbnail < 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_MAX_PER_THUMBNAIL must be >= 0")
	}
	if cfg.FaceMaxUnique < 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_MAX_UNIQUE must be >= 0")
	}
	if cfg.FaceUniquenessThreshold < 0 || cfg.FaceUniquenessThreshold > 64 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_UNIQUENESS_THRESHOLD must be between 0 and 64")
	}
	if cfg.FacePaddingRatio < 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_PADDING_RATIO must be >= 0")
	}
	if cfg.FaceMinSize <= 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_MIN_SIZE must be > 0")
	}
	if cfg.FaceScaleFactor <= 1.0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_SCALE_FACTOR must be > 1.0")
	}
	if cfg.FaceMinNeighbors < 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_MIN_NEIGHBORS must be >= 0")
	}

	detector := strings.ToLower(strings.TrimSpace(cfg.FaceDetector))
	if detector == "" {
		detector = "yunet"
	}
	switch detector {
	case "yunet", "haar":
	default:
		return faceExtractionConfig{}, fmt.Errorf("unsupported FACE_DETECTOR %q (supported: yunet, haar)", cfg.FaceDetector)
	}

	if cfg.FaceYunetScoreThreshold < 0 || cfg.FaceYunetScoreThreshold > 1 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_YUNET_SCORE_THRESHOLD must be between 0 and 1")
	}
	if cfg.FaceYunetNMSThreshold < 0 || cfg.FaceYunetNMSThreshold > 1 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_YUNET_NMS_THRESHOLD must be between 0 and 1")
	}
	if cfg.FaceYunetTopK <= 0 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_YUNET_TOPK must be > 0")
	}
	if cfg.FaceRecognitionThreshold < 0 || cfg.FaceRecognitionThreshold > 1 {
		return faceExtractionConfig{}, fmt.Errorf("FACE_RECOGNITION_THRESHOLD must be between 0 and 1")
	}

	ext := strings.ToLower(strings.TrimSpace(cfg.FaceFormat))
	switch ext {
	case "", "jpg", "jpeg":
		ext = "jpg"
	case "png", "webp":
	default:
		return faceExtractionConfig{}, fmt.Errorf("unsupported FACE_FORMAT %q (supported: jpg, png, webp)", cfg.FaceFormat)
	}

	return faceExtractionConfig{
		Enabled:              true,
		Detector:             detector,
		CascadePath:          strings.TrimSpace(cfg.FaceCascadePath),
		NormalizedWidth:      cfg.FaceNormalizedWidth,
		NormalizedHeight:     cfg.FaceNormalizedHeight,
		MaxPerThumbnail:      cfg.FaceMaxPerThumbnail,
		MaxUnique:            cfg.FaceMaxUnique,
		UniquenessThreshold:  cfg.FaceUniquenessThreshold,
		PaddingRatio:         cfg.FacePaddingRatio,
		MinSize:              cfg.FaceMinSize,
		ScaleFactor:          cfg.FaceScaleFactor,
		MinNeighbors:         cfg.FaceMinNeighbors,
		Ext:                  ext,
		YunetModelPath:       strings.TrimSpace(cfg.FaceYunetModelPath),
		YunetScoreThreshold:  float32(cfg.FaceYunetScoreThreshold),
		YunetNMSThreshold:    float32(cfg.FaceYunetNMSThreshold),
		YunetTopK:            cfg.FaceYunetTopK,
		SFaceModelPath:       strings.TrimSpace(cfg.FaceSFaceModelPath),
		RecognitionThreshold: float32(cfg.FaceRecognitionThreshold),
		WriteGroupsJSON:      true,
	}, nil
}
