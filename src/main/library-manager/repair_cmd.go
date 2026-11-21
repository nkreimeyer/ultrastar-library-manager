package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/repairtxt"
)

// runRepair runs the repair subcommand. It returns the exit code (same meaning as run).
func runRepair(args []string, output io.Writer) int {
	name := filepath.Base(os.Args[0]) + " repair"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	var wordlists, words stringList
	songDir := fs.String("s", "", "Folder with the songs to repair (required); all its undamaged txts form the reference corpus")
	outDir := fs.String("o", "", "Output folder for the report, originals/ and the lyrics cache (default: "+config.DefaultOutDirName+"/ next to -s)")
	dryRun := fs.Bool("n", false, "Dry run: write the report only; no txt is changed")
	fs.Var(&wordlists, "wordlist", "Word list file, \"path\" or \"lang=path\" (de=/usr/share/hunspell/de_DE.dic): one word per line, hunspell .dic and frequency lists work too; repeatable")
	fs.Var(&words, "words", "File of reviewed spellings (Täterätätä): they win over all other evidence, in every song; words below a line \"[Artist - Title]\" apply to that song only, \"[*]\" goes back to all songs; repeatable")
	lyrics := fs.Bool("lyrics", false, "Look up the lyrics of songs that still have unrepaired words at lrclib.net (sends artist and title) and use them as a reference")
	lyricsCache := fs.String("lyrics-cache", "", "Folder for cached lyrics answers, not inside -s (default: <out>/lyrics-cache)")
	strict := fs.Bool("strict", false, "Leave the weak repairs (little evidence) undone; the report lists them with their suggestion")
	logLevel := fs.String("v", "info", "Log level (debug, info, warn, error)")
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: %s -s <song-dir> [-wordlist [lang=]<file>]... [-words <file>]... [-lyrics] [-strict] [-o <out>] [-n] [-v level]\n\n"+
			"Repairs song txts damaged by an earlier conversion, which put U+FFFD (�) in place of\n"+
			"every character it could not decode (\"f�r\", \"I�m\"). Each U+FFFD is replaced by the one\n"+
			"character the file names of the song, its lyrics (-lyrics), the library's undamaged txts\n"+
			"in the song's language and the word lists agree on. Ambiguous words are left as they are.\n"+
			"Only the U+FFFD bytes change; the original of every rewritten txt goes to <out>/originals/.\n"+
			"Review <out>/%s (run with -n first).\n\nFlags:\n", name, repairtxt.ReportTxt)
		fs.PrintDefaults()
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(output, "Error: "+format+"\n\n", a...)
		fs.Usage()
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		return usage("unexpected arguments %q.\nBool flags must be written as -n=false.", fs.Args())
	}
	if *songDir == "" {
		return usage("-s <song-dir> is required.")
	}
	if *lyricsCache != "" && !*lyrics {
		return usage("-lyrics-cache needs -lyrics.")
	}
	opts := repairtxt.Options{Strict: *strict}
	for _, w := range wordlists {
		wl, err := repairtxt.ParseWordListFlag(w)
		if err != nil {
			return usage("%v", err)
		}
		if _, err := os.Stat(wl.Path); err != nil {
			return usage("-wordlist: %v", err)
		}
		opts.WordLists = append(opts.WordLists, wl)
	}
	for _, w := range words {
		if _, err := os.Stat(w); err != nil {
			return usage("-words: %v", err)
		}
		opts.Words = append(opts.Words, w)
	}
	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(output, "Error: invalid log level %q (use debug, info, warn or error).\n", *logLevel)
		return exitUsage
	}
	log.SetLevel(level)

	cfg, err := config.New(*songDir, *outDir, *dryRun)
	if err != nil {
		log.Error(err)
		return exitUsage
	}
	ops := fsops.New(cfg.DryRun)
	if *lyrics {
		cache := *lyricsCache
		if cache == "" {
			cache = filepath.Join(cfg.OutDir, "lyrics-cache")
		}
		// The cache is written in dry runs too.
		if cache, err = cfg.OutsideSongDir(cache); err != nil {
			log.Errorf("-lyrics-cache: %v", err)
			return exitUsage
		}
		opts.Lyrics = repairtxt.NewLRCLib(cache, ops)
	}
	if err := cfg.PrepareOutDir(); err != nil {
		log.Error(err)
		return exitFailures
	}
	if cfg.DryRun {
		log.Infof("Dry run: no txt is changed (the report is written to the output dir)")
	}
	log.Infof("Song dir: %s", cfg.SongDir)
	log.Infof("Output dir: %s", cfg.OutDir)
	runErr := repairtxt.Run(cfg, ops, opts)
	if runErr != nil {
		log.Errorf("repair failed: %v", runErr)
	}
	st := ops.Stats()
	verb := ""
	if cfg.DryRun {
		verb = " (planned)"
	}
	log.Infof("Done%s: %d txts rewritten, %d failed operations; see %s", verb, st.Written, st.Failed, cfg.ReportPath(repairtxt.ReportTxt))
	if st.Failed > 0 || runErr != nil {
		return exitFailures
	}
	return exitOK
}
