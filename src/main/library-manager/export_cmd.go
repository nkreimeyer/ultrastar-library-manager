package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/export"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// runExport runs the export subcommand: the song list of a library as a CSV file. The
// library is only read. It returns the exit code (same meaning as run).
func runExport(args []string, output io.Writer) int {
	name := filepath.Base(os.Args[0]) + " export"
	fs := newSubcommand(name, output)
	library := fs.String("l", "", "Library folder to list the songs of (required)")
	outDir := fs.String("o", "", "Output folder for the CSV (default: "+config.DefaultOrganizeOutDirName+"/ next to -l); not with -csv")
	csvPath := fs.String("csv", "", "Write the CSV to this file instead of <out>/"+export.FileName+"; not inside the library")
	bom := fs.Bool("bom", true, "Start the file with a UTF-8 byte order mark, so that Excel shows umlauts right (disable with -bom=false)")
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: %s -l <library> [-o <out> | -csv <file>] [-bom=false] [-v level]\n\n"+
			"Writes one row per song: Artist, Title, Year, Genre, BPM, Language, Length (s), Duet.\n"+
			"The values come from the song texts; a missing artist or title is taken from an\n"+
			"\"Artist - Title\" folder name. BPM is the #BPM value of the file (a decimal comma becomes a\n"+
			"dot). Length is the time of the end of the last note in seconds (the audio is usually a\n"+
			"few seconds longer); it is empty for a song without notes or #BPM. Duet is yes or no.\n\n"+
			"The separator is a comma. A value that contains a comma, a double quote or a line break is\n"+
			"wrapped in double quotes, and a double quote inside it is written twice (RFC 4180), as\n"+
			"Excel, LibreOffice and Google Sheets expect. The library is only read.\n\nFlags:\n", name)
		fs.PrintDefaults()
	}
	if code, ok := fs.parse(args, boolFlagHint("bom")); !ok {
		return code
	}
	if *library == "" {
		return fs.usageError("-l <library> is required.")
	}
	if *csvPath != "" && *outDir != "" {
		return fs.usageError("-o and -csv cannot be combined: -csv names the file itself.")
	}
	if !fs.setLogLevel() {
		return exitUsage
	}

	cfg, err := organize.NewConfig(organize.ModeDedupe, nil, *library, *outDir, true) // the library must exist
	if err != nil {
		log.Error(err)
		return exitUsage
	}
	path := cfg.ReportPath(export.FileName)
	if *csvPath != "" {
		if path, err = filepath.Abs(*csvPath); err != nil {
			log.Error(err)
			return exitUsage
		}
		if config.IsWithin(path, cfg.Library) {
			log.Errorf("-csv %s is inside the library %s; the file would be taken for a leftover. Choose a file outside of it.", path, cfg.Library)
			return exitUsage
		}
	} else if err := cfg.PrepareOutDir(); err != nil {
		log.Error(err)
		return exitFailures
	}

	ops := fsops.New(true) // nothing in the library is ever written; the CSV is a report
	log.Infof("Library: %s", cfg.Library)
	n, err := export.Run(ops, cfg.Library, path, *bom, export.Options{})
	if err != nil {
		log.Errorf("export failed: %v", err)
	} else {
		log.Infof("Wrote %d songs to %s", n, path)
	}
	st := ops.Stats()
	log.Infof("Done: %d songs, %d failed operations", n, st.Failed)
	return exitStatus(st.Failed > 0 || err != nil)
}
