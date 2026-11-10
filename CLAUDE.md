# CLAUDE.md

Tooling for sanitizing an **UltraStar** karaoke song library. Each song is a folder holding
one `.txt` (UltraStar format: `#TAG:value` header lines plus note lines), an audio file,
and optionally a video, a cover and a background image.

The active code is a Go CLI in `src/main/songs-sanitizer/`. The `old/` directory holds the
earlier Python and shell scripts. They are kept for reference only; `old/` is the only place
duplicate-folder detection exists today.

## Layout

```
src/main/songs-sanitizer/     Go module github.com/nkreimeyer/ultrastar-songs-sanitizer (go 1.24)
  main.go                     flags, validation, runs the modules in fixed order, summary + exit code
  pipeline_test.go            fixtxt → cleanup integration tests (real run, dry-run, idempotence)
  config/                     Config{SongDir, OutDir, DryRun}: path validation, output layout, marker file
  fsops/                      the ONLY place that modifies files: dry-run, atomic writes, moves, stats
  utils/txt.go                txt decoding (DecodeTxt), header tags, song-txt lookup, file references
  utils/folders.go            song folder discovery (GetAllSongFolders)
  utils/utils.go              media type checks, cover/background detection
  fixtxt/                     dedupe song txts, decode → UTF-8, fix #MP3/#AUDIO/#VIDEO/#COVER/#BACKGROUND
  cleanup/                    move unreferenced files to extra-files/, near-empty folders to invalid/
  fixvideos/                  ffmpeg: re-encode to H.264 mp4, downscale >1080p, strip audio, flag corrupt files
  fixaudio/                   ffmpeg: downmix to stereo, peak-normalize to -3 dB (marked, idempotent)
old/                          legacy Python/shell (duplicate detection by folder name, fuzzy txt compare)
test-assets/                  local fixture library (gitignored, not in the repo)
```

Dependencies: `logrus` and `golang.org/x/text` (charmaps, UTF-16, Unicode normalization).
Runtime needs `ffmpeg` ≥ 5.1 and `ffprobe` on PATH for `-m` and `-a`.

## Build, run, test

```bash
cd src/main/songs-sanitizer
gofmt -l . ; go vet ./... && go test -race ./...   # ffmpeg tests skip if ffmpeg is missing
go build -o ../../../test-assets/mybin .
cd ../../../test-assets && ./mybin -s songs -n     # dry run; outputs go to test-assets/sanitizer-output/
```

Flags: `-s <songdir>` (required) · `-o <outdir>` (default `sanitizer-output/` next to `-s`) ·
`-n` dry run · `-t` fixtxt (default **on**) · `-c` cleanup (default **on**) · `-m` fixvideos ·
`-a` fixaudio · `-v <level>`. Order is always fixtxt → cleanup → fixvideos → fixaudio. Bool
flags are turned off with `-t=false`; leftover arguments (`-t false`) are rejected. Exit codes:
0 ok, 1 some operation failed, 2 usage error. See README.md for user-facing details.

### Rules for working on the code

- **All library modifications go through `fsops.Ops`** (`WriteFileAtomic`, `MoveInto`,
  `Rename`, `Replace`, `Remove`, `PlanReplace`). Never call `os.Rename/Remove/WriteFile/Create`
  on library paths. The only exception is removing your own `fsops.TempFileNear` file after
  a failure. This is what makes dry-run, atomic writes and the summary/exit code work.
- **Never delete.** Removed files go to `cfg.ExtraFilesDir()`, and broken folders to
  `cfg.InvalidDir()`, using `ops.MoveInto(src, dir, cfg.RelPath(...))` so the library structure
  is mirrored. Reports go to `ops.WriteReport(cfg.ReportPath(name), …)` with sorted content.
- **Dry-run** must leave `cfg.SongDir` byte-identical. Probes (ffprobe, volumedetect) are
  fine. Planned media changes are recorded with `ops.PlanReplace`.
- **No `log.Fatal`/panic in modules.** Log with context and continue with the next song. Use
  `ops.Fail()` for failures outside `ops`. `Run` only returns an error if the module cannot run.
- **Txt handling**: decode with `utils.DecodeTxt`. Never rewrite a file whose `Problem` is set.
  Match tags as `tag+":"` case-insensitively (`#VIDEO` must not match `#VIDEOGAP`). Find the song
  txt with `utils.FindSongTxt`, which ignores `License.txt` etc. Resolve file names with
  `utils.ResolveFileName`, which is exact first and then case/NFC-insensitive.
- Hidden files (`utils.IsHidden`) are never media. They are leftovers of interrupted runs or
  macOS `._*` files.
- Tests use `t.TempDir()` for both the library and the out dir (`config.New(lib, out, dry)`),
  never `test-assets/`. For ffmpeg tests, generate media with lavfi and call `t.Skip` when
  ffmpeg is missing. When fixing a bug, check that the test fails without the fix (a mutation
  check).
- **Never use `go run .` against a library**, and never point the tool at the real library
  while developing. Use a copy in `test-assets/`.

## Architecture notes

- `config.New` resolves symlinks and refuses an out dir inside the song dir. `PrepareOutDir`
  writes the `.songs-sanitizer-output` marker; discovery skips folders containing it.
- Discovery (`utils.GetAllSongFolders(ops, dir)`): a folder with a song txt is a song.
  - A txt-less folder with songs below it is a category.
  - A folder with a txt or media but no song below it is a broken song, returned as a whole.
  - A category of only broken songs returns them individually.
  - Hidden folders, `@eaDir` and planned dry-run moves are skipped. Unreadable folders are
    logged, counted and skipped.
- Concurrency: fixtxt and cleanup run batches of 100 goroutines, one batch after another.
  fixvideos and fixaudio run one goroutine per batch of 100 folders, capped at 4 / 16
  batches. Per-run state lives in a collector or processor struct with a mutex (no globals).
  `fsops` serializes target-name reservation.
- Cover vs. background is guessed from image shape (square = cover). JPEG, PNG and GIF
  headers are read with `image.DecodeConfig`.

### UltraStar format reminders

The code still ignores most of the format. Keep this in mind when extending it.

- Header tags are case-insensitive in practice. Match `tag+":"`, never a bare prefix.
- Format ≥1.0 (`#VERSION`) requires UTF-8 and deprecates `#ENCODING`. Format ≥1.1 adds
  `#AUDIO` (replaces `#MP3`), `#VOCALS` and `#INSTRUMENTAL`.
- Note lines: `: * F R G` = `<type> <startBeat> <length> <pitch> <syllable>`; `-` = line
  break; `E` = end of song; `P1`/`P2` = duet singers. Lyrics are **split into syllables across
  lines**, so whole words must be rebuilt by joining consecutive syllables.
- `#BPM` and `#GAP` may use a decimal comma (`300,5`). Song time in ms =
  `GAP + beat * 60000 / (BPM * 4)`.

## Known issues

All bugs found in the 2026-11 audit of commit `ed82bee` are fixed: lossy decoding,
swapped `#MP3`/`#VIDEO`, deleting every txt, the `#VIDEOGAP` overwrite, the broken
`invalid/` path, extra-files ending up inside the library, case-sensitive `.txt` checks,
temp files in the CWD, `log.Fatal`, bitrate loss, the extension-change handling, audio
stripped from the only audio source, and the README.

**Design decision**: fixvideos and fixaudio replace media in place, and that cannot be
undone. This is intentional: keeping every original would make the library very large while
the scripts run. Do not add backups of re-encoded media. Undo and journal features only
cover moves and txt edits.

Still open (roadmap items):

- **No signal handling.** Ctrl-C during `-m`/`-a` leaves a hidden `.*.sanitizer-*` temp file.
  It is ignored as media and moved to `extra-files/` by the next cleanup run, and the
  original is untouched.
- **Dry-run is per module.** Later modules don't see txt changes that earlier modules would
  make, so the plan can differ from a real run. Planned folder moves *are* respected
  (`ops.WouldBeMoved`).
- `utils.IsFileReferenced` is a substring search (`a.mp3` matches `banana.mp3`). It errs on
  the side of keeping files. It will be replaced by the txt parser.
- `-c:a copy` into mp4 fails for audio codecs mp4 cannot hold (PCM/WMA inside `.avi`); the
  failure is logged and the original kept. A video whose re-encode is always larger gets re-encoded and discarded
  on every run.
- With fewer than 100 songs, fixvideos processes all videos one at a time (batching).
- Cover vs. background uses exact squareness (600×598 counts as a background).
- `-t` and `-c` are on by default.

## Roadmap

Suggested order: **4 (safety basics) → 2 → 3 → 1 → 5**. Every later step relies on dry-run,
backups and atomic writes being in place. Step 5 is the end goal and needs 1–3, so that only
clean, deduplicated, working songs end up in the final library.

### 1. Improve duplicate detection

Today: Go only "dedupes" multiple `.txt` files *inside one folder*, destructively by mtime.
Cross-folder detection only exists in `old/` and is folder-name based. Its `(?!DUET)` check is
case-sensitive, it scans the top level only, picks a winner by listing order, and crashes if
`songs.dup/<name>` already exists.

- Port it to a Go `dedupe` module that is **report-only by default**. It writes
  `duplicates.txt` (or JSON) with groups and a suggested keeper. Moving files to `songs.dup/`
  happens only behind an explicit flag.
- Identity key from the parsed `#ARTIST`/`#TITLE`, not folder names. Normalize Unicode with
  NFC, casefold, and strip punctuation, `feat./ft.` and bracket/paren qualifiers. Keep the
  qualifiers that change the song (`[DUET]`, `Live`, `Acoustic`, `Remix`, language).
- Fuzzy matching: title similarity within the same artist, plus content similarity of the
  note lines (same BPM and note count → near-certain duplicate). Optionally add an audio
  fingerprint (chromaprint `fpcalc`) for same-audio checks.
- Choose the keeper on quality: txt completeness (has notes, `E`, valid BPM), video
  present/resolution, audio bitrate, cover present, `#VERSION`, no repair warnings.
- Within a folder: keep only the txt that parses as UltraStar, i.e. ignore `License.txt`
  and other non-song txts. Treat duet/alternate txts as variants, not duplicates. Never
  delete if no candidate qualifies.

### 2. Fix encoding checks and broken umlauts (ä ö ü ß …)

**Done in robustness slice 1:**
- [x] strict decoding (UTF-16 BOM → strict UTF-8 → CP1252), with ambiguous files left
  untouched and listed in `encoding_issues.txt`;
- [x] BOM and line-ending normalization, stale `#ENCODING` dropped, writes only when content
  changed;
- [x] NFC-insensitive file references;
- [x] non-UTF-8 file names reported.

**Still open:** mojibake repair and the tiered `U+FFFD` repair described below. Damaged
files are already listed in `encoding_issues.txt`, which can serve as the work list.

The Python fixer *failed* on undecodable bytes and moved on to the next codec. The Go code
silently replaces them. Restore the fail-then-fallback behaviour, in the right order:

1. **Strict UTF-8**: `utf8.Valid(raw)` on the raw bytes. Do not use the `x/text` decoder,
   which never errors. Strip a BOM if present.
2. **CP1252**: reject if the raw bytes contain any of the 5 bytes CP1252 leaves undefined
   (`0x81 0x8D 0x8F 0x90 0x9D`). `charmap.Windows1252` also silently maps these to U+FFFD.
3. **ISO-8859-1**: last resort, since it never fails. Warn if there are bytes in
   `0x80–0x9F` (they become C1 control characters). The old code tried this *before*
   CP1252, which made CP1252 unreachable.
4. Nothing fits → log it, add it to a report, and **do not rewrite the file**.

Also:

- Honour `#ENCODING` as a hint when present. Remove it or set `#VERSION` when writing UTF-8.
- Normalize line endings (`\r\n`, `\r`) before splitting. Drop `bufio.Scanner` (64 KB line
  limit).
- Write only when content changed, atomically (temp file in the same dir, then rename),
  with an optional `.bak`.
- **Mojibake repair**: double-encoded text like `MÃ¤dchen` is valid UTF-8, so the steps
  above won't catch it. Detect typical sequences (`Ã¤ Ã¶ Ã¼ ÃŸ Ã„ â€™ â€ž`) and test
  whether re-encoding as CP1252 and decoding as UTF-8 gives clean text. Apply it only if it
  does.
- **Files already damaged by earlier runs** contain a literal `U+FFFD`. Repair them in tiers:
  1. Dictionary pass (deterministic): for each word containing `�`, rebuilt by joining
     syllables across note lines, try candidates picked by `#LANGUAGE`
     (`ä ö ü Ä Ö Ü ß é è ’ …`) against a wordlist (e.g. hunspell `de_DE`). If exactly one
     matches, auto-fix.
  2. LLM pass for ambiguous cases: send the surrounding lyric lines plus `#ARTIST`, `#TITLE`
     and `#LANGUAGE`. Output is constrained to replacing only the `�` positions; reject any
     answer that changes other characters or note fields.
  3. Optional: compare against lyrics fetched online.

  Default to a dry run that writes a reviewable diff. Map each fix back to the right
  syllable and never touch timing fields.
- Unicode-normalize (NFC) filenames and tag values before comparing them, so references
  with `Mädchen.mp3` match regardless of NFC vs. NFD.

### 3. Detect broken songs in more ways

Today a song is "broken" only if it has ≤1 file, no txt, or no audio and no video.

- Parse the txt properly: `#BPM` present and > 0 (decimal comma allowed), `#GAP` parseable,
  at least N note lines, `E` terminator, consistent `P1`/`P2` sections for duets, beats
  increasing monotonically, no overlapping notes.
- **Duration check**: compute the last note's end time
  (`GAP + (start+len) * 60000 / (BPM*4)`) and compare it with the audio duration from
  ffprobe (`format=duration`; reuse `getVideoDuration`). Flag songs whose notes run past the
  audio end, or that end far earlier (truncated audio or wrong BPM). Same for video vs.
  audio length if `#VIDEOGAP` is set.
- Media integrity: decode check (`ffmpeg -v error -i f -f null -`), zero-length or
  truncated files, audio with no audio stream, unsupported codecs. Add the missing audio
  extensions (`.opus`, `.wma`, `.mka`) so valid songs aren't sent to invalid/.
- Referenced files that don't exist. Matching must be case-insensitive and normalized.
- Images: use `image.DecodeConfig` (no full decode). Support gif/bmp/webp. Use an
  aspect-ratio tolerance plus filename hints (`[CO]`, `[BG]`, `cover`) for the cover vs.
  background decision.
- Output: one structured report (`report.json` plus a readable summary) with a reason per
  song, instead of scattered txt logs. Moving songs to `invalid/` stays opt-in.

### 4. Robustness and UX

Slice 1 is **done** (2026-11):
- [x] dry-run (`-n`), one output dir (`-o`, default `sanitizer-output/`, marker file,
  symlink-safe), and nothing deleted: everything moved, with a mirrored structure and
  collision-free names;
- [x] the `fsops` layer: atomic writes only when content changed, a copy+delete fallback
  across filesystems, permissions preserved, temp files next to their target;
- [x] explicit config instead of `sync.Once` globals, no `log.Fatal`;
- [x] a summary line, exit codes 0/1/2, rejected leftover args, helpful CLI errors;
- [x] robust discovery: unreadable folders skipped, never a whole category as one song;
- [x] one shared song-txt lookup, case/NFC-insensitive references, hidden files ignored;
- [x] keeping the source bitrate, metadata and cover art in fixaudio, plus the idempotence
  marker;
- [x] tests for every package, plus pipeline tests.

Next slices:
- Signal handling: `signal.NotifyContext` in main, a ctx in `Config`, `exec.CommandContext`
  for ffmpeg, and removing the temp file on cancel.
- Parse each song's txt **once** into a `Song` struct (tags with their original case and
  order, note lines, the chosen txt file) that all modules share. This replaces substring
  reference checks and lets dry-run apply earlier modules' planned txt changes.
- CLI: subcommands (`fixtxt`, `cleanup`, `video`, `audio`, `dedupe`, `check`, `organize`) or
  at least opt-in destructive modules, progress output (n/total, ETA), and an undo journal
  for moves and txt edits (not for re-encoded media, see "Design decision").
- Concurrency: one worker pool sized from `runtime.NumCPU()` over files instead of batches of
  100 folders. Remember videos not worth re-encoding (a journal in the out dir).
- Audio: loudness normalization (`loudnorm`, EBU R128) instead of peak normalization.
- CI: `go vet`, `staticcheck` and `go test -race`.

### 5. Final library structure: one folder per starting character

Goal: one consolidated library sorted into a subfolder per starting character, the way
UltraStar Manager organizes songs:

```
<library>/
  0-9/   2 Unlimited - No Limit/
  A/     ABBA - Dancing Queen/
  …
  Z/
  #/     everything else (symbols, non-Latin scripts)
```

- **Sources**: all songs from the three existing subfolders of the current library. Their
  names and layouts are not in this repo yet, so the source folders must be configurable,
  e.g. `organize -s <src1> -s <src2> -s <src3> -o <library>`. Nothing is written into the
  sources; songs are copied or moved into the new tree, journaled and reversible.
- **Bucket rule**: the first character of the normalized `#ARTIST` from the txt, not the
  folder name. Uppercase it and fold diacritics (`Ä` → `A`, `É` → `E`). Digits go to `0-9`,
  anything else to `#`. The rule needs to be a single function with tests. The
  buckets above are confirmed. Optional: ignore a leading `The ` (configurable).
- **Folder name**: `Artist - Title`, built from the tags, with characters that are invalid
  on Windows/macOS/Linux replaced or removed. Keep qualifiers that change the song, such as
  `[DUET]` or `(Live)`. Renaming files inside a song folder (e.g. `Artist - Title.mp3`) is
  optional, and the txt must be updated in the same step (`ReplaceInUSTxtFile`).
- **Collisions across sources** are resolved by the duplicate detection from item 1: keep
  the best version, and send the others to `<out>/duplicates/<source>/…` for review.
- Only songs that pass item 3's checks go into the library. Broken ones go to
  `<out>/invalid/<source>/…` as they do today.
- Workflow: `organize --plan` writes the full mapping (source → target, plus why each
  duplicate or invalid song was excluded) to a report for review. A second run applies
  exactly that plan. The run must be idempotent: running it again on the finished library
  moves nothing.
- `utils.GetAllSongFolders` already handles nested category folders, so the other modules
  keep working on the new `A/…/Song` layout unchanged.
