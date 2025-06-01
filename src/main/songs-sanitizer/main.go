package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixtxt"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixvideos"
	_ "github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
)

func main() {
	// Define command-line flags
	songDir := flag.String("song-dir", "songs", "Path to the folder containing song subfolders")
	runFixVideos := flag.Bool("fix-videos", false, "Run the video-fixing and re-encoding module")
	runFixTxt := flag.Bool("fix-txt", true, "Run the text-file sanitization module")

	flag.Parse()

	// Check if the supplied song folder exists
	absSongDir, err := filepath.Abs(*songDir)
	if err != nil {
		log.Fatalf("Invalid song-dir: %v", err)
	}
	if st, err := os.Stat(absSongDir); err != nil || !st.IsDir() {
		log.Fatalf("song-dir must be a path to an existing directory: %v", err)
	}

	// Run fixtxt module if requested
	if *runFixTxt {
		fmt.Println("Running fixtxt module...")
		if err := fixtxt.Run(absSongDir); err != nil {
			log.Printf("[WARN] fixtxt encountered an error: %v", err)
		}
	}

	// Run fixvideos module if requested
	if *runFixVideos {
		fmt.Println("Running fixvideos module...")
		if err := fixvideos.Run(absSongDir); err != nil {
			log.Printf("[WARN] fixvideos encountered an error: %v", err)
		}
	}
}
