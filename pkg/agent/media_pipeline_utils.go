package agent

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
	"golang.org/x/net/http2"
)

// Constants for shared HTTP client configuration
const (
	// HTTP Client Configuration
	sharedHTTPTimeout = 15 * time.Second

	// Shared connection pooling configuration for both translation and TTS
	sharedMaxIdleConns        = 30 // Increased for both stages
	sharedMaxIdleConnsPerHost = 15 // Higher for shared usage
	sharedIdleConnTimeout     = 90 * time.Second
	sharedTLSHandshakeTimeout = 10 * time.Second
	sharedDialTimeout         = 5 * time.Second
)

var (
	// Shared HTTP client for both translation and TTS stages
	sharedHTTPClient *http.Client
	clientOnce       sync.Once
)

// getSharedHTTPClient returns a singleton HTTP client optimized for OpenAI API calls
// with HTTP/2 support and connection pooling. Used by both translation and TTS stages.
func getSharedHTTPClient() *http.Client {
	clientOnce.Do(func() {
		// Create custom transport with HTTP/2 support and connection pooling
		transport := &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   sharedDialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: sharedTLSHandshakeTimeout,
			IdleConnTimeout:     sharedIdleConnTimeout,
			MaxIdleConns:        sharedMaxIdleConns,
			MaxIdleConnsPerHost: sharedMaxIdleConnsPerHost,
			DisableCompression:  false,
			ForceAttemptHTTP2:   true,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}

		// Configure HTTP/2 transport
		if err := http2.ConfigureTransport(transport); err != nil {
			getLogger := logger.GetLogger()
			getLogger.Warnw("failed to configure shared HTTP/2 transport", err)
		}

		sharedHTTPClient = &http.Client{
			Transport: transport,
			Timeout:   sharedHTTPTimeout,
		}
	})
	return sharedHTTPClient
}
