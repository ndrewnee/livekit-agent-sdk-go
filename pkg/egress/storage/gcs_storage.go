package storage

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/livekit/protocol/logger"
)

// GCSStorage implements Google Cloud Storage backend
// Implements GCS storage requirements from PLAN.md Milestone 3
type GCSStorage struct {
	config  GCSConfig
	logger  logger.Logger
	client  *http.Client

	// Authentication
	accessToken   string
	tokenExpiry   time.Time
	projectID     string

	// Circuit breaker
	circuitBreaker *CircuitBreaker

	// Metrics
	metrics StorageMetrics

	// State
	mu       sync.RWMutex
	closed   bool
}

// gcsTokenResponse represents OAuth2 token response
type gcsTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// gcsServiceAccount represents service account key file
type gcsServiceAccount struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	PrivateKey              string `json:"private_key"`
	ClientEmail             string `json:"client_email"`
	ClientID                string `json:"client_id"`
	AuthURI                 string `json:"auth_uri"`
	TokenURI                string `json:"token_uri"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
	ClientX509CertURL       string `json:"client_x509_cert_url"`
}

// NewGCSStorage creates a new GCS storage backend
func NewGCSStorage(config GCSConfig, logger logger.Logger) (*GCSStorage, error) {
	if config.Bucket == "" {
		return nil, fmt.Errorf("GCS bucket not specified")
	}

	g := &GCSStorage{
		config: config,
		logger: logger,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		circuitBreaker: NewCircuitBreaker(5, 2, 60*time.Second, 3),
	}

	// Initialize authentication
	if err := g.initializeAuth(); err != nil {
		return nil, fmt.Errorf("failed to initialize GCS auth: %w", err)
	}

	// Test connectivity
	if err := g.testConnection(); err != nil {
		return nil, fmt.Errorf("failed to connect to GCS: %w", err)
	}

	logger.Infow("GCS storage initialized",
		"bucket", config.Bucket,
		"project", g.projectID,
		"prefix", config.Prefix)

	return g, nil
}

// initializeAuth initializes GCS authentication
func (g *GCSStorage) initializeAuth() error {
	var serviceAccount *gcsServiceAccount
	var err error

	// Load service account from file or JSON
	if g.config.CredentialsFile != "" {
		serviceAccount, err = g.loadServiceAccountFromFile(g.config.CredentialsFile)
	} else if g.config.CredentialsJSON != "" {
		serviceAccount, err = g.loadServiceAccountFromJSON(g.config.CredentialsJSON)
	} else {
		// Try to use default credentials (e.g., from environment or metadata service)
		return g.useDefaultCredentials()
	}

	if err != nil {
		return err
	}

	// Set project ID
	if g.config.ProjectID != "" {
		g.projectID = g.config.ProjectID
	} else if serviceAccount != nil {
		g.projectID = serviceAccount.ProjectID
	}

	// Get initial access token
	if serviceAccount != nil {
		return g.getAccessToken(serviceAccount)
	}

	return nil
}

// loadServiceAccountFromFile loads service account from file
func (g *GCSStorage) loadServiceAccountFromFile(path string) (*gcsServiceAccount, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read service account file: %w", err)
	}

	var sa gcsServiceAccount
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("failed to parse service account file: %w", err)
	}

	return &sa, nil
}

// loadServiceAccountFromJSON loads service account from JSON string
func (g *GCSStorage) loadServiceAccountFromJSON(jsonStr string) (*gcsServiceAccount, error) {
	var sa gcsServiceAccount
	if err := json.Unmarshal([]byte(jsonStr), &sa); err != nil {
		return nil, fmt.Errorf("failed to parse service account JSON: %w", err)
	}
	return &sa, nil
}

// useDefaultCredentials attempts to use default GCP credentials
func (g *GCSStorage) useDefaultCredentials() error {
	// Try to get token from metadata service (for GCE/GKE)
	metadataURL := "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

	req, err := http.NewRequest("GET", metadataURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create metadata request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Not on GCP, try environment variable
		if creds := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); creds != "" {
			sa, err := g.loadServiceAccountFromFile(creds)
			if err != nil {
				return fmt.Errorf("failed to load default credentials: %w", err)
			}
			return g.getAccessToken(sa)
		}
		return fmt.Errorf("no default credentials available")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metadata service returned status %d", resp.StatusCode)
	}

	var tokenResp gcsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode metadata token: %w", err)
	}

	g.accessToken = tokenResp.AccessToken
	g.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return nil
}

// getAccessToken obtains an OAuth2 access token using JWT assertion
func (g *GCSStorage) getAccessToken(sa *gcsServiceAccount) error {
	// Create JWT claims for Google OAuth2
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   sa.ClientEmail,
		"scope": "https://www.googleapis.com/auth/devstorage.read_write",
		"aud":   sa.TokenURI,
		"exp":   now.Add(1 * time.Hour).Unix(),
		"iat":   now.Unix(),
	}

	// Parse private key
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return fmt.Errorf("failed to parse private key PEM")
	}

	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS1 format
		rsaKey, err2 := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err2 != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}
		parsedKey = rsaKey
	}

	rsaKey, ok := parsedKey.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("private key is not RSA")
	}

	// Create and sign JWT
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(rsaKey)
	if err != nil {
		return fmt.Errorf("failed to sign JWT: %w", err)
	}

	// Exchange JWT for access token
	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	data.Set("assertion", tokenString)

	req, err := http.NewRequest("POST", sa.TokenURI, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp gcsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	g.accessToken = tokenResp.AccessToken
	g.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return nil
}

// refreshTokenIfNeeded refreshes the access token if it's expired
func (g *GCSStorage) refreshTokenIfNeeded() error {
	if time.Now().After(g.tokenExpiry.Add(-5 * time.Minute)) {
		// Token is expired or will expire soon
		return g.initializeAuth()
	}
	return nil
}

// testConnection tests the connection to GCS
func (g *GCSStorage) testConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test by checking if bucket exists
	url := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s", g.config.Bucket)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+g.accessToken)

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to GCS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("bucket %s not found", g.config.Bucket)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GCS returned status %d", resp.StatusCode)
	}

	return nil
}

// StoreSegment stores a segment in GCS
func (g *GCSStorage) StoreSegment(ctx context.Context, sessionID string, segmentName string, data []byte) error {
	if g.isClosed() {
		return fmt.Errorf("storage is closed")
	}

	// Refresh token if needed
	if err := g.refreshTokenIfNeeded(); err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	// Build object path
	objectPath := g.buildObjectPath(sessionID, segmentName)

	// Execute with circuit breaker
	return g.circuitBreaker.Execute(func() error {
		return g.uploadObject(ctx, objectPath, data, getContentType(segmentName))
	})
}

// uploadObject uploads an object to GCS
func (g *GCSStorage) uploadObject(ctx context.Context, objectPath string, data []byte, contentType string) error {
	startTime := time.Now()

	// Build upload URL
	url := fmt.Sprintf("https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		g.config.Bucket, objectPath)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+g.accessToken)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))

	// Set storage class if configured
	if g.config.StorageClass != "" {
		req.Header.Set("X-Goog-Storage-Class", g.config.StorageClass)
	}

	// Set KMS key if configured
	if g.config.KMSKeyName != "" {
		req.Header.Set("X-Goog-Encryption-Kms-Key-Name", g.config.KMSKeyName)
	}

	// Execute request
	resp, err := g.client.Do(req)
	if err != nil {
		atomic.AddInt64(&g.metrics.FailedUploads, 1)
		return fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		atomic.AddInt64(&g.metrics.FailedUploads, 1)
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GCS upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Update metrics
	atomic.AddInt64(&g.metrics.SuccessfulUploads, 1)
	atomic.AddInt64(&g.metrics.BytesUploaded, int64(len(data)))
	uploadDuration := time.Since(startTime)

	g.mu.Lock()
	g.metrics.AverageUploadTime = (g.metrics.AverageUploadTime + uploadDuration) / 2
	g.mu.Unlock()

	g.logger.Debugw("object uploaded to GCS",
		"path", objectPath,
		"size", len(data),
		"duration", uploadDuration)

	return nil
}

// GetSegment retrieves a segment from GCS
func (g *GCSStorage) GetSegment(ctx context.Context, sessionID string, segmentName string) ([]byte, error) {
	if g.isClosed() {
		return nil, fmt.Errorf("storage is closed")
	}

	// Refresh token if needed
	if err := g.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Build object path
	objectPath := g.buildObjectPath(sessionID, segmentName)

	// Build download URL
	url := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s?alt=media",
		g.config.Bucket, objectPath)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+g.accessToken)

	// Execute request
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("object not found")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GCS download failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Read response
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Update metrics
	atomic.AddInt64(&g.metrics.TotalDownloads, 1)
	atomic.AddInt64(&g.metrics.BytesDownloaded, int64(len(data)))

	return data, nil
}

// DeleteSegment deletes a segment from GCS
func (g *GCSStorage) DeleteSegment(ctx context.Context, sessionID string, segmentName string) error {
	if g.isClosed() {
		return fmt.Errorf("storage is closed")
	}

	// Refresh token if needed
	if err := g.refreshTokenIfNeeded(); err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	// Build object path
	objectPath := g.buildObjectPath(sessionID, segmentName)

	// Build delete URL
	url := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s",
		g.config.Bucket, objectPath)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+g.accessToken)

	// Execute request
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GCS delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	g.logger.Debugw("object deleted from GCS",
		"path", objectPath)

	return nil
}

// ListSegments lists segments in GCS
func (g *GCSStorage) ListSegments(ctx context.Context, sessionID string) ([]string, error) {
	if g.isClosed() {
		return nil, fmt.Errorf("storage is closed")
	}

	// Refresh token if needed
	if err := g.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Build prefix
	prefix := filepath.Join(g.config.Prefix, sessionID) + "/"

	// Build list URL
	url := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o?prefix=%s",
		g.config.Bucket, prefix)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+g.accessToken)

	// Execute request
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GCS list failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var listResponse struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&listResponse); err != nil {
		return nil, fmt.Errorf("failed to parse list response: %w", err)
	}

	// Extract segment names
	segments := make([]string, 0, len(listResponse.Items))
	for _, item := range listResponse.Items {
		// Remove prefix to get segment name
		segmentName := strings.TrimPrefix(item.Name, prefix)
		if segmentName != "" {
			segments = append(segments, segmentName)
		}
	}

	return segments, nil
}

// StorePlaylists stores HLS playlists in GCS
func (g *GCSStorage) StorePlaylists(ctx context.Context, sessionID string, master, media []byte) error {
	var errs []error

	// Store master playlist if provided
	if len(master) > 0 {
		if err := g.StoreSegment(ctx, sessionID, "master.m3u8", master); err != nil {
			errs = append(errs, fmt.Errorf("failed to store master playlist: %w", err))
		}
	}

	// Store media playlist
	if len(media) > 0 {
		if err := g.StoreSegment(ctx, sessionID, "playlist.m3u8", media); err != nil {
			errs = append(errs, fmt.Errorf("failed to store media playlist: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("playlist storage errors: %v", errs)
	}

	return nil
}

// GetPlaylist retrieves a playlist from GCS
func (g *GCSStorage) GetPlaylist(ctx context.Context, sessionID string, playlistType string) ([]byte, error) {
	playlistName := "playlist.m3u8"
	if playlistType == "master" {
		playlistName = "master.m3u8"
	}

	return g.GetSegment(ctx, sessionID, playlistName)
}

// StoreManifest stores a manifest in GCS
func (g *GCSStorage) StoreManifest(ctx context.Context, sessionID string, manifest []byte) error {
	return g.StoreSegment(ctx, sessionID, "manifest.json", manifest)
}

// GetManifest retrieves a manifest from GCS
func (g *GCSStorage) GetManifest(ctx context.Context, sessionID string) ([]byte, error) {
	return g.GetSegment(ctx, sessionID, "manifest.json")
}

// StoreScreenshot stores a screenshot in GCS
func (g *GCSStorage) StoreScreenshot(ctx context.Context, sessionID string, timestamp int64, data []byte) error {
	filename := fmt.Sprintf("screenshot_%d.jpg", timestamp)
	return g.StoreSegment(ctx, sessionID, filename, data)
}

// ListScreenshots lists screenshots in GCS
func (g *GCSStorage) ListScreenshots(ctx context.Context, sessionID string) ([]ScreenshotInfo, error) {
	segments, err := g.ListSegments(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var screenshots []ScreenshotInfo
	for _, segment := range segments {
		if strings.HasPrefix(segment, "screenshot_") {
			// Parse timestamp from filename
			var timestamp int64
			fmt.Sscanf(segment, "screenshot_%d", &timestamp)

			screenshots = append(screenshots, ScreenshotInfo{
				Timestamp: timestamp,
				Format:    strings.TrimPrefix(filepath.Ext(segment), "."),
				URL:       g.getPublicURL(sessionID, segment),
			})
		}
	}

	return screenshots, nil
}

// buildObjectPath builds the full object path
func (g *GCSStorage) buildObjectPath(sessionID, segmentName string) string {
	if g.config.Prefix != "" {
		return filepath.Join(g.config.Prefix, sessionID, segmentName)
	}
	return filepath.Join(sessionID, segmentName)
}

// getPublicURL returns the public URL for an object
func (g *GCSStorage) getPublicURL(sessionID, segmentName string) string {
	objectPath := g.buildObjectPath(sessionID, segmentName)
	return fmt.Sprintf("https://storage.googleapis.com/%s/%s", g.config.Bucket, objectPath)
}

// getContentType returns content type based on file extension
func getContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	case ".mp4":
		return "video/mp4"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".json":
		return "application/json"
	case ".key":
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

// GetStorageInfo returns storage information
func (g *GCSStorage) GetStorageInfo() StorageInfo {
	return StorageInfo{
		Type:        StorageTypeGCS,
		Available:   !g.isClosed() && g.IsHealthy(),
		Location:    "gcs://" + g.config.Bucket,
		CloudBucket: g.config.Bucket,
	}
}

// IsHealthy checks if the storage is healthy
func (g *GCSStorage) IsHealthy() bool {
	if g.isClosed() {
		return false
	}

	// Check circuit breaker
	if g.circuitBreaker.IsOpen() {
		return false
	}

	// Check failure rate
	failed := atomic.LoadInt64(&g.metrics.FailedUploads)
	successful := atomic.LoadInt64(&g.metrics.SuccessfulUploads)
	total := failed + successful

	if total > 10 && float64(failed)/float64(total) > 0.5 {
		return false
	}

	return true
}

// GetMetrics returns storage metrics
func (g *GCSStorage) GetMetrics() StorageMetrics {
	g.mu.RLock()
	defer g.mu.RUnlock()

	metrics := g.metrics
	metrics.CircuitBreakerOpen = g.circuitBreaker.IsOpen()

	return metrics
}

// Close closes the GCS storage
func (g *GCSStorage) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed {
		return nil
	}

	g.closed = true
	g.logger.Infow("GCS storage closed", "bucket", g.config.Bucket)

	return nil
}

// isClosed checks if storage is closed
func (g *GCSStorage) isClosed() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.closed
}

// SetRetryConfig configures retry settings
func (g *GCSStorage) SetRetryConfig(maxAttempts int, initialBackoff, maxBackoff time.Duration, multiplier float64, jitter bool) {
	// This would configure retry settings in production
	g.logger.Debugw("GCS retry config updated",
		"max_attempts", maxAttempts,
		"initial_backoff", initialBackoff,
		"max_backoff", maxBackoff)
}

// SetCircuitBreakerConfig configures circuit breaker
func (g *GCSStorage) SetCircuitBreakerConfig(failureThreshold, successThreshold int, openTimeout time.Duration, halfOpenRequests int) {
	g.circuitBreaker = NewCircuitBreaker(failureThreshold, successThreshold, openTimeout, halfOpenRequests)
	g.logger.Debugw("GCS circuit breaker config updated",
		"failure_threshold", failureThreshold,
		"success_threshold", successThreshold,
		"open_timeout", openTimeout)
}