package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/cleanup"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixaudio"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixtxt"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixvideos"
	_ "github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"

	log "github.com/sirupsen/logrus"
)

func main() {
	// Define command-line flags
	songDir := flag.String("s", "", "Path to the folder containing song sub-folders")
	runFixTxt := flag.Bool("t", true, "Run the text-file sanitization module")
	runCleanup := flag.Bool("c", true, "Run the cleanup module")
	runFixVideos := flag.Bool("m", false, "Run the video-fixing and re-encoding module")
	runFixAudio := flag.Bool("a", false, "Run the audio-fixing and re-encoding module")
	logLevel := flag.String("v", "info", "Log level (debug, info, warn, error, fatal, panic)")

	flag.Parse()

	// if songDir is not provided, print usage information and exit
	if *songDir == "" {
		flag.Usage() // Print usage information to std
		return
	}

	// Set log level
	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		log.Fatalf("Invalid log level: %v", err)
	}
	log.SetLevel(level)

	// Check if the supplied song folder exists
	absSongDir, err := filepath.Abs(*songDir)
	if err != nil {
		log.Fatalf("Invalid song-dir: %v", err)
		return
	}
	if st, err := os.Stat(absSongDir); err != nil || !st.IsDir() {
		log.Fatalf("song-dir must be a path to an existing directory: %v", err)
		return
	}

	if *runFixTxt {
		log.Infof("Running fixtxt module...")
		if err := fixtxt.Run(absSongDir, *logLevel); err != nil {
			log.Warnf("fixtxt encountered an error: %v", err)
		}
	}

	if *runCleanup {
		log.Infof("Running cleanup module...")
		cleanup.Run(absSongDir, *logLevel)
	}

	if *runFixVideos {
		log.Infof("Running fixvideos module...")
		if err := fixvideos.Run(absSongDir, *logLevel); err != nil {
			log.Warnf("fixvideos encountered an error: %v", err)
		}
	}

	if *runFixAudio {
		log.Infof("Running fixaudio module...")
		if err := fixaudio.Run(absSongDir, *logLevel); err != nil {
			log.Warnf("fixaudio encountered an error: %v", err)
		}
	}

	log.Infof("Done.")
}
