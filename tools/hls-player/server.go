package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	dir := flag.String("dir", ".", "Directory to serve files from")
	flag.Parse()

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("Failed to get absolute path: %v", err)
	}

	fmt.Printf("🎥 HLS Player Server\n")
	fmt.Printf("Serving files from: %s\n", absDir)
	fmt.Printf("Listening on: http://localhost:%d\n", *port)
	fmt.Printf("\n")
	fmt.Printf("Quick links:\n")
	fmt.Printf("  Player: http://localhost:%d/tools/hls-player/player.html\n", *port)
	fmt.Printf("  Test output: Look for playlist.m3u8 in /tmp directories\n")
	fmt.Printf("\n")

	// Custom file server with CORS headers for HLS
	fs := http.FileServer(http.Dir(absDir))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Add CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		// Set proper content types for HLS files
		if filepath.Ext(r.URL.Path) == ".m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		} else if filepath.Ext(r.URL.Path) == ".ts" {
			w.Header().Set("Content-Type", "video/mp2t")
		}

		// Handle OPTIONS requests for CORS preflight
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Log requests
		log.Printf("%s %s", r.Method, r.URL.Path)

		fs.ServeHTTP(w, r)
	})

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
