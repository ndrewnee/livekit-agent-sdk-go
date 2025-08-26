// test_runner.go - Main entry point for integration test
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func mainTestRunner() {
	// Parse command line flags
	var (
		mode      = flag.String("mode", "test", "Mode: test, agent, or publisher")
		roomName  = flag.String("room", "", "Room name (for agent or publisher mode)")
		audioFile = flag.String("audio", "../../audiobook.wav", "Audio file path")
	)
	flag.Parse()

	switch *mode {
	case "test":
		// Run complete integration test
		fmt.Println("Running integration test...")
		if err := RunIntegrationTest(); err != nil {
			log.Fatal(err)
		}

	case "agent":
		// Run just the agent (for manual testing)
		if *roomName == "" {
			log.Fatal("Room name required for agent mode")
		}
		fmt.Printf("Running agent for room: %s\n", *roomName)
		// Agent main() will handle this
		os.Args = []string{os.Args[0]} // Reset args for main()
		main()                         // Call the agent's main

	case "publisher":
		// Run just the publisher (for manual testing)
		if *roomName == "" {
			log.Fatal("Room name required for publisher mode")
		}
		fmt.Printf("Publishing to room: %s\n", *roomName)
		// Publisher will be called directly

	default:
		log.Fatalf("Unknown mode: %s", *mode)
	}
}
