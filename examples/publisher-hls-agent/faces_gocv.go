//go:build gocv

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"math/bits"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gocv.io/x/gocv"
)

const (
	defaultYuNetModelFilename = "face_detection_yunet_2023mar.onnx"
	defaultYuNetModelURL      = "https://github.com/opencv/opencv_zoo/raw/refs/heads/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx"
	defaultSFaceModelFilename = "face_recognition_sface_2021dec.onnx"
	defaultSFaceModelURL      = "https://github.com/opencv/opencv_zoo/raw/refs/heads/main/models/face_recognition_sface/face_recognition_sface_2021dec.onnx"
)

type faceUniquenessFilter struct {
	threshold int
	hashes    []uint64
}

func newFaceUniquenessFilter(threshold int) *faceUniquenessFilter {
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 64 {
		threshold = 64
	}
	return &faceUniquenessFilter{threshold: threshold}
}

func (f *faceUniquenessFilter) IsUnique(hash uint64) bool {
	for _, existing := range f.hashes {
		if bits.OnesCount64(hash^existing) <= f.threshold {
			return false
		}
	}
	return true
}

func (f *faceUniquenessFilter) Add(hash uint64) {
	f.hashes = append(f.hashes, hash)
}

type faceIdentityGroup struct {
	ID         int
	Exemplars  []gocv.Mat
	FaceFiles  []string
	PrimaryRef string
}

func (g *faceIdentityGroup) Close() {
	for _, m := range g.Exemplars {
		m.Close()
	}
	g.Exemplars = nil
}

type facesGroupsManifest struct {
	Version               int                  `json:"version"`
	Generator             string               `json:"generator"`
	RecognitionModel      string               `json:"recognition_model"`
	DetectionModel        string               `json:"detection_model"`
	RecognitionThreshold  float32              `json:"recognition_threshold"`
	DetectionScoreMinimum float32              `json:"detection_score_minimum"`
	Groups                []facesGroupManifest `json:"groups"`
}

type facesGroupManifest struct {
	ID             int      `json:"id"`
	Representative string   `json:"representative"`
	Faces          []string `json:"faces"`
}

func findOrCreateFaceGroup(fr *gocv.FaceRecognizerSF, groups *[]*faceIdentityGroup, feature gocv.Mat, threshold float32) *faceIdentityGroup {
	if fr == nil {
		return nil
	}
	if groups == nil {
		return nil
	}

	bestGroup := -1
	bestScore := float32(-1)

	for i, g := range *groups {
		for _, exemplar := range g.Exemplars {
			score := fr.MatchWithParams(feature, exemplar, gocv.FaceRecognizerSFDisTypeCosine)
			if score > bestScore {
				bestScore = score
				bestGroup = i
			}
		}
	}

	if bestGroup >= 0 && bestScore >= threshold {
		return (*groups)[bestGroup]
	}

	id := len(*groups)
	g := &faceIdentityGroup{ID: id}
	*groups = append(*groups, g)
	return g
}

func isTooSimilarToGroup(fr *gocv.FaceRecognizerSF, group *faceIdentityGroup, feature gocv.Mat, dedupThreshold float32) bool {
	if fr == nil || group == nil {
		return false
	}
	if dedupThreshold <= 0 {
		return false
	}
	for _, exemplar := range group.Exemplars {
		score := fr.MatchWithParams(feature, exemplar, gocv.FaceRecognizerSFDisTypeCosine)
		if score >= dedupThreshold {
			return true
		}
	}
	return false
}

func extractAndSaveUniqueFacesFromImages(ctx context.Context, imagePaths []string, outputDir string, cfg faceExtractionConfig) (faceExtractionSummary, error) {
	if !cfg.Enabled || len(imagePaths) == 0 {
		return faceExtractionSummary{}, nil
	}

	facesDir := facesOutputDir(outputDir)
	if err := os.MkdirAll(facesDir, 0o755); err != nil {
		return faceExtractionSummary{}, fmt.Errorf("create faces dir %s: %w", facesDir, err)
	}

	useYuNet := strings.EqualFold(cfg.Detector, "yunet")

	var (
		classifier gocv.CascadeClassifier
	)
	if !useYuNet {
		cascadePath, err := resolveCascadePath(cfg.CascadePath)
		if err != nil {
			return faceExtractionSummary{}, err
		}

		classifier = gocv.NewCascadeClassifier()
		if ok := classifier.Load(cascadePath); !ok {
			classifier.Close()
			return faceExtractionSummary{}, fmt.Errorf("failed to load face cascade from %s", cascadePath)
		}
		defer classifier.Close()
	}

	var (
		yunetModel string
		sfaceModel string
		fd         gocv.FaceDetectorYN
		fr         gocv.FaceRecognizerSF
		dnnReady   bool
	)
	if useYuNet {
		var err error
		yunetModel, err = ensureFaceModel(ctx, cfg.YunetModelPath, defaultYuNetModelFilename, defaultYuNetModelURL)
		if err != nil {
			return faceExtractionSummary{}, err
		}
		sfaceModel, err = ensureFaceModel(ctx, cfg.SFaceModelPath, defaultSFaceModelFilename, defaultSFaceModelURL)
		if err != nil {
			return faceExtractionSummary{}, err
		}
		fr = gocv.NewFaceRecognizerSF(sfaceModel, "")
		defer fr.Close()
	}

	filter := newFaceUniquenessFilter(cfg.UniquenessThreshold)
	nextIndex := 0
	summary := faceExtractionSummary{}

	minSize := image.Pt(cfg.MinSize, cfg.MinSize)
	maxSize := image.Pt(0, 0)

	var groups []*faceIdentityGroup
	defer func() {
		for _, g := range groups {
			g.Close()
		}
	}()

	for _, imgPath := range imagePaths {
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		img := gocv.IMRead(imgPath, gocv.IMReadColor)
		if img.Empty() {
			img.Close()
			continue
		}

		gray := gocv.NewMat()
		gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)
		gocv.EqualizeHist(gray, &gray)

		if useYuNet {
			gray.Close()

			if !dnnReady {
				inputSize := image.Pt(img.Cols(), img.Rows())
				fd = gocv.NewFaceDetectorYNWithParams(yunetModel, "", inputSize, cfg.YunetScoreThreshold, cfg.YunetNMSThreshold, cfg.YunetTopK, 0, 0)
				dnnReady = true
				defer fd.Close()
			} else {
				fd.SetInputSize(image.Pt(img.Cols(), img.Rows()))
			}

			faces := gocv.NewMat()
			fd.Detect(img, &faces)

			type yunetDet struct {
				row   int
				area  float32
				score float32
			}
			var detections []yunetDet
			for i := 0; i < faces.Rows(); i++ {
				cols := faces.Cols()
				if cols < 5 {
					continue
				}
				w := faces.GetFloatAt(i, 2)
				h := faces.GetFloatAt(i, 3)
				score := faces.GetFloatAt(i, cols-1)
				detections = append(detections, yunetDet{
					row:   i,
					area:  w * h,
					score: score,
				})
			}
			summary.Detected += len(detections)

			sort.Slice(detections, func(i, j int) bool { return detections[i].area > detections[j].area })
			if cfg.MaxPerThumbnail > 0 && len(detections) > cfg.MaxPerThumbnail {
				detections = detections[:cfg.MaxPerThumbnail]
			}

			for _, det := range detections {
				if cfg.MaxUnique > 0 && summary.Saved >= cfg.MaxUnique {
					summary.SkippedLimit++
					continue
				}

				cols := faces.Cols()
				if cols < 15 {
					summary.FilteredNonFace++
					continue
				}
				score := faces.GetFloatAt(det.row, cols-1)
				if score < cfg.YunetScoreThreshold {
					summary.FilteredNonFace++
					continue
				}
				if !isPlausibleYuNetDetection(faces, det.row, img.Cols(), img.Rows()) {
					summary.FilteredNonFace++
					continue
				}

				faceBox := faces.RowRange(det.row, det.row+1)
				aligned := gocv.NewMat()
				fr.AlignCrop(img, faceBox, &aligned)
				faceBox.Close()
				if aligned.Empty() {
					aligned.Close()
					summary.FilteredNonFace++
					continue
				}

				feature := gocv.NewMat()
				fr.Feature(aligned, &feature)
				if feature.Empty() {
					aligned.Close()
					feature.Close()
					summary.FilteredNonFace++
					continue
				}

				normalized := gocv.NewMat()
				gocv.Resize(aligned, &normalized, image.Pt(cfg.NormalizedWidth, cfg.NormalizedHeight), 0, 0, gocv.InterpolationArea)
				aligned.Close()

				hash, err := dhash64(normalized)
				if err != nil {
					normalized.Close()
					feature.Close()
					continue
				}
				if !filter.IsUnique(hash) {
					summary.SkippedDuplicate++
					normalized.Close()
					feature.Close()
					continue
				}

				var group *faceIdentityGroup
				groupDir := ""
				if cfg.WriteGroupsJSON {
					group = findOrCreateFaceGroup(&fr, &groups, feature, cfg.RecognitionThreshold)
					if group == nil {
						normalized.Close()
						feature.Close()
						faces.Close()
						img.Close()
						return summary, fmt.Errorf("failed to assign face to identity group")
					}
					if isTooSimilarToGroup(&fr, group, feature, cfg.GroupDedupThreshold) {
						summary.SkippedSimilar++
						normalized.Close()
						feature.Close()
						continue
					}

					group.Exemplars = append(group.Exemplars, feature.Clone())

					groupDir = filepath.Join(facesDir, fmt.Sprintf("person%03d", group.ID))
					if err := os.MkdirAll(groupDir, 0o755); err != nil {
						normalized.Close()
						feature.Close()
						faces.Close()
						img.Close()
						return summary, fmt.Errorf("create group dir %s: %w", groupDir, err)
					}
				} else {
					groupDir = facesDir
				}

				filter.Add(hash)

				outName := fmt.Sprintf("face%05d.%s", nextIndex, cfg.Ext)
				outPath := filepath.Join(groupDir, outName)
				if ok := gocv.IMWrite(outPath, normalized); !ok {
					normalized.Close()
					feature.Close()
					faces.Close()
					img.Close()
					return summary, fmt.Errorf("failed to write face image %s", outPath)
				}
				normalized.Close()

				rel := filepath.ToSlash(filepath.Join(facesDirName, filepath.Base(groupDir), outName))
				if groupDir == facesDir {
					rel = filepath.ToSlash(filepath.Join(facesDirName, outName))
				}
				if cfg.WriteGroupsJSON {
					group.FaceFiles = append(group.FaceFiles, rel)
					if group.PrimaryRef == "" {
						group.PrimaryRef = rel
					}
				}
				summary.Files = append(summary.Files, rel)
				summary.Saved++
				nextIndex++
				feature.Close()
			}

			faces.Close()
			img.Close()
			continue
		}

		rects := classifier.DetectMultiScaleWithParams(gray, cfg.ScaleFactor, cfg.MinNeighbors, 0, minSize, maxSize)
		gray.Close()

		sort.Slice(rects, func(i, j int) bool {
			return rectArea(rects[i]) > rectArea(rects[j])
		})
		if cfg.MaxPerThumbnail > 0 && len(rects) > cfg.MaxPerThumbnail {
			rects = rects[:cfg.MaxPerThumbnail]
		}

		summary.Detected += len(rects)

		for _, r := range rects {
			if cfg.MaxUnique > 0 && summary.Saved >= cfg.MaxUnique {
				summary.SkippedLimit++
				continue
			}

			expanded := expandRect(r, img.Cols(), img.Rows(), cfg.PaddingRatio)
			if expanded.Dx() < 2 || expanded.Dy() < 2 {
				continue
			}

			roi := img.Region(expanded)
			face := roi.Clone()
			roi.Close()

			normalized := gocv.NewMat()
			gocv.Resize(face, &normalized, image.Pt(cfg.NormalizedWidth, cfg.NormalizedHeight), 0, 0, gocv.InterpolationArea)
			face.Close()

			hash, err := dhash64(normalized)
			if err != nil {
				normalized.Close()
				continue
			}

			if !filter.IsUnique(hash) {
				summary.SkippedDuplicate++
				normalized.Close()
				continue
			}
			filter.Add(hash)

			outName := fmt.Sprintf("face%05d.%s", nextIndex, cfg.Ext)
			outPath := filepath.Join(facesDir, outName)
			if ok := gocv.IMWrite(outPath, normalized); !ok {
				normalized.Close()
				img.Close()
				return summary, fmt.Errorf("failed to write face image %s", outPath)
			}
			normalized.Close()

			summary.Saved++
			summary.Files = append(summary.Files, filepath.ToSlash(filepath.Join(facesDirName, outName)))
			nextIndex++
		}

		img.Close()
	}

	if cfg.WriteGroupsJSON && useYuNet {
		summary.Groups = len(groups)
		manifestPath := filepath.Join(facesDir, facesGroupsManifestName)
		if err := writeFacesGroupsManifest(manifestPath, yunetModel, sfaceModel, cfg.RecognitionThreshold, cfg.YunetScoreThreshold, groups); err != nil {
			return summary, err
		}
		summary.Files = append(summary.Files, filepath.ToSlash(filepath.Join(facesDirName, facesGroupsManifestName)))
	}

	return summary, nil
}

func resolveCascadePath(cascadePath string) (string, error) {
	if cascadePath != "" {
		if _, err := os.Stat(cascadePath); err != nil {
			return "", fmt.Errorf("FACE_CASCADE_PATH points to missing file %s: %w", cascadePath, err)
		}
		return cascadePath, nil
	}

	if prefix, err := pkgConfigVar("opencv4", "prefix"); err == nil && prefix != "" {
		candidates := []string{
			filepath.Join(prefix, "share", "opencv4", "haarcascades", "haarcascade_frontalface_default.xml"),
			filepath.Join(prefix, "share", "opencv", "haarcascades", "haarcascade_frontalface_default.xml"),
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}

	candidates := []string{
		"/opt/homebrew/share/opencv4/haarcascades/haarcascade_frontalface_default.xml",
		"/usr/local/share/opencv4/haarcascades/haarcascade_frontalface_default.xml",
		"/usr/share/opencv4/haarcascades/haarcascade_frontalface_default.xml",
		"/usr/local/share/opencv/haarcascades/haarcascade_frontalface_default.xml",
		"/usr/share/opencv/haarcascades/haarcascade_frontalface_default.xml",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("FACE_CASCADE_PATH is empty and no OpenCV haarcascade file was found; set FACE_CASCADE_PATH")
}

func pkgConfigVar(pkg, variable string) (string, error) {
	pkgConfig, err := exec.LookPath("pkg-config")
	if err != nil {
		return "", fmt.Errorf("pkg-config not found: %w", err)
	}
	cmd := exec.Command(pkgConfig, "--variable="+variable, pkg)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pkg-config %s %s: %w", pkg, variable, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func rectArea(r image.Rectangle) int {
	return r.Dx() * r.Dy()
}

func expandRect(r image.Rectangle, maxW, maxH int, paddingRatio float64) image.Rectangle {
	if paddingRatio <= 0 {
		return clampRect(r, maxW, maxH)
	}

	padX := int(float64(r.Dx()) * paddingRatio)
	padY := int(float64(r.Dy()) * paddingRatio)

	expanded := image.Rect(r.Min.X-padX, r.Min.Y-padY, r.Max.X+padX, r.Max.Y+padY)
	return clampRect(expanded, maxW, maxH)
}

func clampRect(r image.Rectangle, maxW, maxH int) image.Rectangle {
	x0 := r.Min.X
	y0 := r.Min.Y
	x1 := r.Max.X
	y1 := r.Max.Y
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > maxW {
		x1 = maxW
	}
	if y1 > maxH {
		y1 = maxH
	}
	if x1 < x0 {
		x1 = x0
	}
	if y1 < y0 {
		y1 = y0
	}
	return image.Rect(x0, y0, x1, y1)
}

func dhash64(img gocv.Mat) (uint64, error) {
	if img.Empty() {
		return 0, fmt.Errorf("empty image")
	}

	var gray gocv.Mat
	if img.Channels() == 1 {
		gray = img.Clone()
	} else {
		gray = gocv.NewMat()
		gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)
	}
	defer gray.Close()

	small := gocv.NewMat()
	defer small.Close()
	gocv.Resize(gray, &small, image.Pt(9, 8), 0, 0, gocv.InterpolationArea)
	if small.Empty() {
		return 0, fmt.Errorf("failed to resize image for dhash")
	}

	var hash uint64
	var bit uint
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			left := small.GetUCharAt(y, x)
			right := small.GetUCharAt(y, x+1)
			if left > right {
				hash |= 1 << bit
			}
			bit++
		}
	}
	return hash, nil
}

func ensureFaceModel(ctx context.Context, configuredPath, filename, url string) (string, error) {
	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err != nil {
			return "", fmt.Errorf("face model file missing at %s: %w", configuredPath, err)
		}
		return configuredPath, nil
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		return "", fmt.Errorf("FACE model path not set and no user cache dir available; set FACE_YUNET_MODEL/FACE_SFACE_MODEL")
	}

	modelDir := filepath.Join(cacheDir, "publisher-hls-agent", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return "", fmt.Errorf("create model cache dir %s: %w", modelDir, err)
	}

	dst := filepath.Join(modelDir, filename)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}

	if err := downloadFile(ctx, url, dst); err != nil {
		return "", fmt.Errorf("download model %s: %w", filename, err)
	}

	return dst, nil
}

func downloadFile(ctx context.Context, url, dst string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "publisher-hls-agent/face-model-downloader")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close file: %w", closeErr)
	}

	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename model: %w", err)
	}

	return nil
}

func isPlausibleYuNetDetection(faces gocv.Mat, row, imgW, imgH int) bool {
	cols := faces.Cols()
	if row < 0 || row >= faces.Rows() || cols < 15 {
		return false
	}

	x := faces.GetFloatAt(row, 0)
	y := faces.GetFloatAt(row, 1)
	w := faces.GetFloatAt(row, 2)
	h := faces.GetFloatAt(row, 3)
	if w <= 0 || h <= 0 {
		return false
	}

	x1 := x + w
	y1 := y + h
	if x < 0 || y < 0 || x1 > float32(imgW) || y1 > float32(imgH) {
		return false
	}

	aspect := w / h
	if aspect < 0.5 || aspect > 2.0 {
		return false
	}

	// YuNet columns: bbox (0-3), 5 landmarks (4-13), score (14)
	leX, leY := faces.GetFloatAt(row, 4), faces.GetFloatAt(row, 5)
	reX, reY := faces.GetFloatAt(row, 6), faces.GetFloatAt(row, 7)
	nX, nY := faces.GetFloatAt(row, 8), faces.GetFloatAt(row, 9)
	lmX, lmY := faces.GetFloatAt(row, 10), faces.GetFloatAt(row, 11)
	rmX, rmY := faces.GetFloatAt(row, 12), faces.GetFloatAt(row, 13)

	points := [][2]float32{{leX, leY}, {reX, reY}, {nX, nY}, {lmX, lmY}, {rmX, rmY}}
	for _, p := range points {
		if p[0] < x || p[0] > x1 || p[1] < y || p[1] > y1 {
			return false
		}
	}

	if leX >= reX || lmX >= rmX {
		return false
	}
	if leY >= lmY || reY >= rmY {
		return false
	}
	if !(nY > maxFloat32(leY, reY) && nY < minFloat32(lmY, rmY)) {
		return false
	}

	return true
}

func assignFaceToGroup(fr *gocv.FaceRecognizerSF, groups *[]*faceIdentityGroup, feature gocv.Mat, threshold float32) int {
	if fr == nil {
		return 0
	}

	bestGroup := -1
	bestScore := float32(-1)

	for i, g := range *groups {
		for _, exemplar := range g.Exemplars {
			score := fr.MatchWithParams(feature, exemplar, gocv.FaceRecognizerSFDisTypeCosine)
			if score > bestScore {
				bestScore = score
				bestGroup = i
			}
		}
	}

	if bestGroup >= 0 && bestScore >= threshold {
		(*groups)[bestGroup].Exemplars = append((*groups)[bestGroup].Exemplars, feature.Clone())
		return (*groups)[bestGroup].ID
	}

	id := len(*groups)
	g := &faceIdentityGroup{
		ID:        id,
		Exemplars: []gocv.Mat{feature.Clone()},
	}
	*groups = append(*groups, g)
	return id
}

func writeFacesGroupsManifest(path, yunetModel, sfaceModel string, recognitionThreshold, scoreThreshold float32, groups []*faceIdentityGroup) error {
	manifest := facesGroupsManifest{
		Version:               1,
		Generator:             "publisher-hls-agent",
		RecognitionModel:      filepath.Base(sfaceModel),
		DetectionModel:        filepath.Base(yunetModel),
		RecognitionThreshold:  recognitionThreshold,
		DetectionScoreMinimum: scoreThreshold,
	}

	for _, g := range groups {
		if len(g.FaceFiles) == 0 {
			continue
		}
		manifest.Groups = append(manifest.Groups, facesGroupManifest{
			ID:             g.ID,
			Representative: g.PrimaryRef,
			Faces:          append([]string(nil), g.FaceFiles...),
		})
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal groups manifest: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write groups manifest %s: %w", path, err)
	}

	return nil
}

func maxFloat32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func minFloat32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
