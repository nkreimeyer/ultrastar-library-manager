# CLAUDE.md

Tooling for sanitizing an **UltraStar** karaoke song library. Each song is a folder holding
one `.txt` (UltraStar format: `#TAG:value` header lines plus note lines), an audio file,
and optionally a video, a cover and a background image.

The active code is a Go CLI in `src/main/library-manager/`. The `old/` directory holds the
earlier Python and shell scripts. They are kept for reference only. Cross-source duplicate
detection exists in the Go `organize` subcommand; the folder-name based detection in `old/`
is superseded.

## Layout

```
src/main/library-manager/     Go module github.com/nkreimeyer/library-manager (go 1.24)
  main.go                     flags, validation, runs the modules in fixed order, summary + exit code;
                              os.Args[1] == "organize"/"dedupe"/"add" dispatches to the subcommand
  organize_cmd.go             the organize, dedupe and add subcommands (organize.Mode): own FlagSet, -s tier lists, -undo, validation
  main_test.go                flag validation (-deep/-move-broken need -k), module order, organize usage
  pipeline_test.go            fixtxt → cleanup (→ check) integration tests (real run, dry-run = real-run plan, idempotence)
  config/                     Config{SongDir, OutDir, DryRun}: path validation, output layout, marker file;
                              organize.go: OrganizeConfig{Sources (tier, label), Library, OutDir}; NewDedupe, NewAdd
  fsops/                      the ONLY place that modifies files: dry-run (planned writes/moves visible via ReadFile), atomic writes, moves, stats;
                              MoveTo (exact target, ordered dry-run moves), AppendJournal, RemoveEmptyDir (undo)
  utils/decode.go             txt decoding (DecodeTxt): UTF-16/UTF-8/#ENCODING/CP1250/CP1252, "when in doubt, Problem"
  utils/txt.go                header tags, song-txt lookup, file references
  utils/folders.go            song folder discovery (GetAllSongFolders)
  utils/utils.go              media type checks, non-UTF-8 media names
  utils/songname.go           TagsFromFolderName: obvious #ARTIST/#TITLE fills from "Artist - Title" folder names
  utils/image.go              image headers (JPEG/PNG/GIF via stdlib, own BMP/WebP readers), cover/background choice
  songtxt/                    UltraStar txt parser (tags, notes, breaks, duets, RELATIVE, B lines, BeatToMs); pure
  fixtxt/                     dedupe song txts, decode → UTF-8, fix #MP3/#AUDIO/#VIDEO/#COVER/#BACKGROUND, fill #ARTIST/#TITLE from the folder name
  cleanup/                    move unreferenced files to extra-files/, near-empty folders to invalid/
  check/                      broken-song detection: analyze.go (pure), probe.go (ffprobe/ffmpeg), report.go;
                              inspect.go: Inspector (one folder -> findings, broken, MediaSummary), used by Run and organize
  fixvideos/                  ffmpeg: re-encode to H.264 mp4, downscale to fit 1920x1080, strip audio, flag corrupt files (tagged, idempotent)
  fixaudio/                   ffmpeg: downmix to stereo, peak-normalize to -3 dB, originals kept (tagged, idempotent)
  journal/                    content-fingerprint journal for media formats that cannot carry a marker tag
  organize/                   organize/dedupe/add: name.go (Bucket, FolderName, NameKey), key.go (SongKey, LooseKey, StripCopySuffix),
                              content.go (ContentHash, union-find joining of groups by content),
                              similar.go (Profile, findSimilar, similarJoin, review evidence),
                              quality.go (keeper order), plan.go (MakePlan, orderMoves; pure), organize.go (Run, Undo),
                              journal.go (organize-journal.jsonl), report.go (organize_plan.json/.txt)
old/                          legacy Python/shell (duplicate detection by folder name, fuzzy txt compare)
test-assets/                  local fixture library (gitignored, not in the repo)
```

Dependencies: `logrus` and `golang.org/x/text` (charmaps, UTF-16, Unicode normalization).
Runtime needs `ffmpeg` ≥ 5.1 and `ffprobe` on PATH for `-m` and `-a`.

## Build, run, test

```bash
cd src/main/library-manager
gofmt -l . ; go vet ./... && go test -race ./...   # ffmpeg tests skip if ffmpeg is missing
go build -o ../../../test-assets/mybin .
cd ../../../test-assets && ./mybin -s songs -n     # dry run; outputs go to test-assets/sanitizer-output/
```

Flags: `-s <songdir>` (required) · `-o <outdir>` (default `sanitizer-output/` next to the resolved `-s`) ·
`-n` dry run · `-t` fixtxt (default **on**) · `-c` cleanup (default **on**) · `-k` check
(`-deep`, `-move-broken` only with `-k`) · `-m` fixvideos · `-a` fixaudio · `-v <level>`.
Order is always fixtxt → cleanup → check → fixvideos → fixaudio. Bool
flags are turned off with `-t=false`; leftover arguments (`-t false`) are rejected. Exit codes:
0 ok, 1 some operation failed, 2 usage error. See README.md for user-facing details.

Subcommands: `organize -s <dir>[:<dir>…] [-s …] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-n]`,
`dedupe -l <library> [-o <out>] [-deep] [-n]` (in place: the library is the only source, keepers stay),
`add -s … -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-n]` (the library is always the first
source of the first tier, library keepers stay, new keepers go to their bucket),
and `<cmd> -undo [-o <out> | -l <library>] [-n]`
(one journal for all three). Each `-s` is one priority tier (folders in
it split with `filepath.SplitList`); `-o` defaults to `organize-output/` next to the library.
Same exit codes and leftover-argument rule.

### Rules for working on the code

- **All library modifications go through `fsops.Ops`** (`WriteFileAtomic`, `MoveInto`,
  `MoveTo`, `Rename`, `Replace`, `Remove`, `RemoveEmptyDir`, `PlanReplace`). Never call `os.Rename/Remove/WriteFile/Create`
  on library paths. The only exception is removing your own `fsops.TempFileNear` file after
  a failure. This is what makes dry-run, atomic writes and the summary/exit code work.
- **Never delete.** Removed files go to `cfg.ExtraFilesDir()`, and broken folders to
  `cfg.InvalidDir()`, using `ops.MoveInto(src, dir, cfg.RelPath(...))` so the library structure
  is mirrored. Reports go to `ops.WriteReport(cfg.ReportPath(name), …)` with sorted content.
  The only exception is fixvideos (see "Design decisions"), which deletes the original of a
  conversion with `ops.Remove`, and only after the txt references the new file. organize
  moves only (`<out>/duplicates/<label>/`, `<out>/invalid/<label>/`, the library), journals
  every move, and its undo removes only the empty folders its run created.
- **Dry-run** must leave `cfg.SongDir` byte-identical. Probes (ffprobe, volumedetect) are
  fine. Planned media changes are recorded with `ops.PlanReplace`.
- **Read txts through `ops.ReadFile`** (and the ops-aware `utils.IsSongTxt`, `SongTxtFiles`,
  `FindSongTxt`, `ReadSongTags`, `IsFileReferenced`, `ReplaceFileReference`,
  `FindCoverImage`/`FindBackgroundImage`; `nil` ops = plain disk reads). In a dry run
  `ops.ReadFile` returns the content of a planned `WriteFileAtomic` and reports a file that
  is planned to be moved away as not existing, and the lookups skip such txts, so later
  modules plan what a real run does. Never base a decision on `os.ReadFile` of a library txt.
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
- Mutation testing (`~/go/bin/gremlins`): always run it inside a memory cgroup, one package at
  a time: `systemd-run --user --scope -q -p MemoryMax=6G -p MemorySwapMax=0 -p OOMPolicy=continue
  gremlins unleash --workers 2 --timeout-coefficient 30 ./<pkg>` (80 for fixvideos; the root
  package with `-E '^[a-z]+/' .`). Some mutants (e.g. in `utils.utf8Sequences`) become endless
  allocating loops that OOM the machine otherwise. Never pass `--test-cpu`: it makes every
  mutant count as KILLED. Tokens in switch `case` expressions are always NOT COVERED (a
  coverage-profile artifact); check those by hand. Surviving mutants that are equivalent
  (unique sort keys, log-only arguments, buffer sizes) are expected.
- **Never use `go run .` against a library**, and never point the tool at the real library
  while developing. Use a copy in `test-assets/`.

## Architecture notes

- `config.New` resolves symlinks and refuses an out dir inside the song dir. `PrepareOutDir`
  writes the `.library-manager-output` marker; discovery skips folders containing it (and the
  legacy `.songs-sanitizer-output` of the old program name). The program was called
  songs-sanitizer until 2026-11-14; the media tags (`SANITIZER_*`), the default
  `sanitizer-output/` and `organize-output/` folders and the temp-file names keep their names, so
  existing journals and tagged media stay valid.
- Discovery (`utils.GetAllSongFolders(ops, dir)`): a folder with a song txt is a song.
  - A txt-less folder with songs below it is a category.
  - A folder with a txt or media but no song below it is a broken song, returned as a whole.
  - A category of only broken songs returns them individually.
  - Hidden folders, `@eaDir` and planned dry-run moves are skipped. Unreadable folders are
    logged, counted and skipped. A folder holding the output marker counts as content, so
    its parent is never returned as a broken song.
  - Symlinks to regular txt files are song txts (`ListTxtFiles`/`FindSongTxt` and discovery
    agree). fixtxt never rewrites a symlinked txt; it lists it in `missing_files.txt` instead.
- Header tags (`utils.HeaderTags`, `songtxt.Parse`) are read up to the first body line
  (note, `-`, `B`, `P`); tags after it are ignored (`IssueTagAfterNotes`). A repeated tag
  resolves to its last header occurrence, as in UltraStar. A lone `\r` is a line break.
- `utils.DecodeTxt`: any NUL byte is a Problem (BOM-less UTF-16 is still decoded, so the
  header stays readable). With an `#ENCODING` tag, only "clean" UTF-8 sequences count as
  mixed-encoding evidence, and in non-Latin codepages an isolated 2-byte sequence never does.
  `#LANGUAGE` names are diacritic-folded (`Čeština`, `Română`).
- fsops: a move across file systems first checks that src can be renamed (fails cleanly if
  not), copies, then parks src under a hidden `.fsops-move-<pid>-<n>` name and deletes it.
  If that delete fails, the move still counts as done and the leftover is logged.
  `AppendJournal` starts a new line after a crash-truncated last line. Collision names keep
  dotfiles intact (`.DS_Store (2)`).
- Concurrency: fixtxt and cleanup run batches of 100 goroutines, one batch after another.
  fixaudio uses a worker pool of min(16, NumCPU) over folders, and fixvideos one of 4 workers
  over video files. Per-run state lives in a collector or processor struct with a mutex (no
  globals). `fsops` serializes target-name reservation.
- Moving a folder onto an existing target merges it in two cases: this run already moved
  something below that target (a leftover category whose songs were just moved), or the
  folder is empty. A song folder colliding with one from an earlier run gets ` (2)`.
- Folders whose media or image names are not valid UTF-8 are skipped entirely by fixtxt
  (and their files are never moved by cleanup). They are reported until renamed, because a
  CP1252 txt may reference them byte-for-byte.
- Cover vs. background (`utils.FindCoverImage`/`FindBackgroundImage`): filename hints first
  (`[CO]`/`[BG]` strong; the words `cover`/`background`/`bg` weak and ignored when they are
  part of the folder name, `#ARTIST` or `#TITLE`), then shape (`utils.IsSquareImage`:
  within 5 %). An image is never both. Image headers come from `utils.ReadImageHeader`;
  errors wrap `ErrUnsupportedImage` (TIFF, HEIF, …) or `ErrInvalidImage`.
- organize: discovery per source, `check.Inspector` in a worker pool (min(16, NumCPU)), then
  `MakePlan` (pure, table-tested): encoding problems and unreadable folders stay, broken songs
  go to invalid/, working songs are grouped by `SongKey` (duet flag from P1/P2 or a `[DUET]`
  title), groups sharing a `contentKey` (`ContentHash` + duet flag) are joined by union-find
  (group key = smallest SongKey; a loser whose SongKey differs from the keeper's gets
  `joinedByContent` appended to its reason; a broken song without a SongKey match finds its
  replacement by content), then pairs from `findSimilar` that pass `similarJoin` are joined
  too (`joinedBySimilar`; the other pairs become `Plan.Similar` review entries with
  evidence), and the keeper is chosen by `chooseKeeper`: the criteria (not a `_dupl<n>` copy, tier
  with `-priority`, song warnings, video, video bitrate ≤ `fixvideos.BitrateThreshold`, height capped at
  `fixvideos.MaxHeight`, audio bitrate within 10 % of the group's best, cover, background,
  `#VERSION`, source order, path) are filters over the whole group, never pairwise
  comparisons, because the bitrate tolerance is not transitive. Each loser's reason is the
  filter that dropped it. Without ffprobe, `MediaSummary.HasVideo` only means `#VIDEO`
  names an existing, non-empty file (height and bitrate unknown = 0). `SongKey` folds diacritics only on Latin/Greek letters (marks of
  other scripts make different letters), drops invisible format characters like FolderName,
  and never lets a `feat.` part or a duet qualifier swallow a version word (`Live`, `Remix`,
  …). `LooseKey` groups for "possible duplicates" are split by the duet flag: a duet and a
  solo version are both kept (the duet as `… [DUET]`) and never listed together. Target
  `<Bucket>/<FolderName>`; NameKey collisions of different songs get ` (2)`,
  ` (3)` from one run-wide claimed set (a number never takes another song's plain name;
  keepers already in place keep their number). A target taken by a non-candidate or a
  staying song, or a bucket that is a file, a song folder (song txt or media directly in
  it) or unreadable (`ops.FailOnce`), leaves the keeper in place. Apply: out-dir moves
  first (`MoveInto`), then library moves with `MoveTo` in `orderMoves` order (targets freed
  first, cycles and self-nesting through a hidden `.organize-tmp-*` name); right before each
  library move the bucket is checked again (an earlier move away of a song folder named like
  the bucket may have failed). The journal (`organize-journal.jsonl`: `run`, `mkdir`,
  `move`, `undone`) is appended and fsynced per entry, only in real runs; a journal write
  failure stops all further moves, and a song parked under the temporary name is moved back
  (or, if its place is taken, to its target) so it never stays hidden. `-undo` walks the
  newest not-undone run backwards. A dry run executes the same steps through ops (MoveTo
  simulates the ordered moves), so its plan and move count match the real run
  (`TestRunDryRunMatchesRealRun`).
- check: `analyze` is a pure function over the parsed song and probed file info, so every
  rule is table-tested without ffmpeg. Findings are not `ops.Fail`s and don't change the
  exit code. Repeated tags resolve like UltraStar does (the last header occurrence wins;
  fixtxt's `removeDuplicateTags` also keeps the last and only looks at the header). Songs
  whose txt has a decode Problem are reported but never moved by `-move-broken`.
  `mediaInfo.HasVideo` means a moving video stream (attached cover art does not count).
  `VideoBitrate` comes from the stream `bit_rate`, else its `BPS`/`BPS-eng` tag (Matroska),
  else the container rate of a file without audio; 0 = unknown.
- organize: when a keeper from another source cannot reach the library, the library's own
  copies of that song stay in place (not moved to duplicates/). The report's duplicate and
  invalid `Dest` is the actual target (with ` (2)`), also in dry runs.
- organize `_dupl<n>` copies (`StripCopySuffix`, case-insensitive, at the end): `newCandidate`
  strips it from `#ARTIST`/`#TITLE` (so keys and folder names ignore it) and sets
  `MarkedCopy` if the folder, the song txt, `#AUDIO`/`#MP3`/`#VIDEO` (extension ignored) or
  a tag carries it. A marked copy loses first, before the tier; a lone copy is kept.
- dedupe/add plan with `PlanOptions.LibraryInPlace`: a library keeper's target is its own
  `Rel` (ActInPlace) and its NameKey is claimed before other targets are numbered (a family
  whose plain name a staying library song holds is numbered from ` (2)`); a non-candidate at
  a target is never a song (`reasonNotASong`); a broken library song without a working
  version stays (`reasonBrokenStays`), and one whose replacement input is left stays too
  (`keepLibraryLosers`). Broken inputs always go to invalid/. `NewAdd` prepends the library
  (if it exists) to the first tier, so it is source 0 and wins ties; giving it as `-s` is an
  error. `NewDedupe` requires an existing library.

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

**Design decisions:**
- fixvideos replaces videos in place, and that cannot be undone. This is intentional:
  keeping every original video would make the library very large while the scripts run.
  Do not add backups of re-encoded videos. The same goes for conversions that change the
  name (`.avi` → `.mp4`): the original is deleted once the txt points to the new file.
- fixaudio **does** keep a copy of every source file it re-encodes, in `<out>/originals/`
  (`ops.CopyInto`, before `ops.Replace`). Each file is processed only once, thanks to the
  marker tag or journal, so it is backed up only once.
- Re-encoding must be idempotent. Mark processed or checked media with a tag
  (`SANITIZER_NORMALIZED`, `SANITIZER_VIDEO`). Formats that cannot hold one go into the
  content-fingerprint journal (`journal`, `<out>/media-journal.txt`), which survives renames
  and moves (e.g. the A–Z reorganization). `.m4a` files are journaled, not tagged: writing
  the tag needs `-movflags +use_metadata_tags`, which drops the cover and the iTunes tags.
- fixvideos re-encodes always output 8-bit yuv420p with even dimensions. A conversion
  whose `.mp4` target already exists is skipped before encoding.

Still open (roadmap items):

- **No signal handling.** Ctrl-C during `-m`/`-a` leaves a hidden `.*.sanitizer-*` temp file.
  It is ignored as media and moved to `extra-files/` by the next cleanup run, and the
  original is untouched.
- **Dry-run simulates txts and moves, not media.** Later modules see planned txt writes
  (`ops.ReadFile`) and planned moves away (`ops.WouldBeMoved`, planned merge targets), so
  the fixtxt → cleanup → check plan matches a real run (`TestPipelineDryRunMatchesRealRun`).
  Not simulated: media changes planned by fixvideos/fixaudio (`PlanReplace` only logs and
  counts them, and the `#VIDEO` rewrite of an extension-changing conversion is not recorded
  as a planned txt write); no later module depends on them today (fixaudio, the only module
  after fixvideos, reads only audio files and `#VOCALS`/`#INSTRUMENTAL`). Also not
  simulated: moves or renames *into* a path, and new files from planned writes in directory
  listings (`os.ReadDir` and `utils.ResolveFileName` list the disk). A future module that
  reads `#VIDEO` after fixvideos would need `PlanReplace` to record the txt change.
- Peak normalization of heavily clipped material at low bitrates can miss -3 dBFS by about
  1–2 dB on the first pass (encoder overshoot). The marker prevents repeated passes;
  `loudnorm` would be the better fix.
- `utils.IsFileReferenced` is a substring search (`a.mp3` matches `banana.mp3`). It errs on
  the side of keeping files. It will be replaced by the txt parser.
- `-c:a copy` into mp4 fails for audio codecs mp4 cannot hold (PCM/WMA inside `.avi`); the
  failure is logged and the original kept.
- `#ARTIST`/`#TITLE` missing is a check error (UltraStar Deluxe refuses such songs). fixtxt
  fills them from an obvious `Artist - Title` folder name (`utils.TagsFromFolderName`, shared
  with check; fills are merged into `filled_tags.txt`, which keeps the written fills of all
  runs and replaces a dry run's planned lines). fixtxt finds the tag lines the way songtxt
  does (`nameTagLine`: leading blanks and blanks around the key ignored, last occurrence),
  so `#ARTIST :x` counts as present. check names the fill in its message (seen only with
  `-t=false`: otherwise check reads the filled txt, in a dry run the planned one). Songs whose folder
  name is not obvious (no or several ` - `, reversed naming, a prefix before the artist,
  a track-number artist) still need a manual fix.
- check: without `-xerror`, ffmpeg exits 0 on truncated/damaged files, so `-deep` reports
  them as `decode-warning`; `decode-error` only fires when ffmpeg cannot open the file.
- check: `check_report.*` is regenerated each run, so moves done by `-move-broken` are only
  in that run's report and the log (an undo journal is in item 4).
- check: an empty folder inside a category is reported as `no-song-txt` (error).
- fixaudio drops cover attachments of `.mka` files (kept in `originals/`). Codecs it cannot
  re-encode as themselves (DTS, TrueHD, and HE-AAC/HE-AACv2, since ffmpeg's native aac
  encoder only writes AAC-LC) are skipped with a warning, so such files are not downmixed.
- `fsops.WriteFileAtomic` (and so `utils.ReplaceFileReference`, used by fixvideos) replaces
  a symlinked txt with a regular file; only fixtxt guards against it.
- fixvideos: in a dry run, a re-encode that would come out larger is still planned as
  "re-encode + strip audio" (the real run falls back to a strip-only stream copy).
- `-t` and `-c` are on by default.
- organize: a keeper whose target is an existing non-song folder in the library (e.g. an
  emptied category named exactly `A/Artist - Title`) stays in place until that folder is
  removed by hand. Empty category folders left in the sources are counted, not removed.
  A song parked under `.organize-tmp-*` whose final move and move-back both fail stays
  hidden until `-undo` (if the journal failed too, it must be moved by hand; the log names
  it). A completed move whose journal append fails cannot be undone (the journal records
  moves after they happen).
- organize: in the plain text of a title, `ft.` is not a feat marker in `SongKey` (it may
  be `Ft. Lauderdale`); `Song ft. X` and `Song` are only listed as possible duplicates.

## Roadmap

Suggested order: **4 (safety basics) → 2 → 3 → 1 → 5**. Every later step relies on dry-run,
backups and atomic writes being in place. Step 5 is the end goal and needs 1–3, so that only
clean, deduplicated, working songs end up in the final library.

### 1. Improve duplicate detection

**Partly done** (2026-11) inside `organize`: cross-source duplicates are grouped by a key from
the parsed `#ARTIST`/`#TITLE` (`organize.SongKey`: NFC, casefold, diacritics, punctuation,
`feat.`, known-noise qualifiers stripped, other qualifiers kept, duets as variants), the keeper
is chosen on quality (`organize.chooseKeeper`), the losers are moved to `<out>/duplicates/`, and
`LooseKey` lists possible duplicates for review. `dedupe` removes duplicates of one library in
place (`-n` is the report-only mode), `add` adds sources to a library without re-bucketing it, and
`_dupl<n>` copies always lose. Exact content duplicates are joined by `organize.ContentHash`
(SHA-256 over the last `#BPM`, `B` lines and the sorted notes: type, start, length, pitch,
NFC syllable, singer; `#GAP`, other tags, line breaks, RELATIVE, encoding ignored; fewer than
10 notes = no hash). Test fixtures must give different songs different notes (`notesTxt(lyric)`),
or the hash joins them. The similarity check (`organize/similar.go`) joins the same chart with
small edits: lyrics Jaccard over 4-word shingles ≥ 0.9 (line breaks end words), chart in ms
from the first note within 30 ms with the pitch relative to the first note mod 12, similar
artists (`artistSimilar`), same `variantWords`; candidates come from an inverted shingle
index (shingles in > 50 songs ignored) and a coarse 50 ms chart key. Measured on the 15,147-song
library (2026-11-14, txt-only shadow copy, no ffprobe): exact content 90 duplicates, plus the
similarity check 195 (no false merge found in review). **Still open:**
re-timed charts (another tempo) are only listed, never joined; artist aliases (`30`/`Thirty Seconds
to Mars`, `CCR`), placeholder artists, U+FFFD as a wildcard, German `ae`/`ä` spellings; audio fingerprints, and choosing between several song txts *inside one folder* by content.

Before organize: Go only "dedupes" multiple song `.txt` files *inside one folder*: it keeps the
newest and moves the others to `extra-files/` (by mtime only, no content comparison).
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
- Normalize line endings (`\r\n`, `\r`) before splitting (done).
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

**Done** (2026-11) as the `check` module (`-k`, report-only; `-move-broken` opt-in, `-deep`
opt-in) on top of the new `songtxt` parser:
- [x] txt structure: tags, `#BPM`/`#GAP` (decimal comma), notes, `E`, duet sections
  (`P1`/`P2`/`P3`), monotonic beats per section, overlaps per singer, RELATIVE and `B` lines;
- [x] durations: notes past the audio end (estimated mp3 lengths are measured first), lyrics
  ending early, video too short (with `#VIDEOGAP`), `#START`/`#END`;
- [x] media integrity: missing/zero-length/unreadable files, missing streams, `-deep` full
  decode; `.mka` audio;
- [x] images: own BMP/WebP header readers, 5 % squareness tolerance, filename hints;
- [x] `check_report.json` + `check_report.txt` with a reason code per finding.

Still open from this item: the other modules still write their own reports
(`missing_files.txt`, `encoding_issues.txt`, `potentiallyCorruptedFiles.txt`), and fixtxt/cleanup
still move structurally broken songs (no txt, no media) to `invalid/` by default.

Original plan:

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
  extension `.mka` (`.opus` and `.wma` are done) so valid songs aren't sent to invalid/.
- Referenced files that don't exist. Matching must be case-insensitive and normalized.
- Images: use `image.DecodeConfig` (no full decode). Support gif/bmp/webp. Use an
  aspect-ratio tolerance plus filename hints (`[CO]`, `[BG]`, `cover`) for the cover vs.
  background decision.
- Output: one structured report (`report.json` plus a readable summary) with a reason per
  song, instead of scattered txt logs. Moving songs to `invalid/` should become opt-in.

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

Slice 2 is **done** (2026-11):
- [x] fixaudio keeps originals (`originals/`), measures the true float peak (astats),
  skips `#VOCALS`/`#INSTRUMENTAL` stems, and uses a worker pool;
- [x] idempotent video processing (`SANITIZER_VIDEO` tag, lossless remux to tag discarded
  results, journal for AVI/MPEG), downscaling of tall videos, a worker pool over files;
- [x] the media journal (content fingerprints, saved after every entry, atomic writes);
- [x] decoding, moved to `utils/decode.go` ("when in doubt, Problem"): `#ENCODING` hints for
  all Windows/ISO/KOI8 codepages (Latin-1 is read as CP1252, unknown codecs are a Problem);
  CP1250 via `#LANGUAGE`; CP1250 artefacts are detected; `classifyUTF8Sequences` separates
  accidental UTF-8 (isolated sequences such as `ß…`) from real UTF-8 (runs, Latin-1 leads,
  `’ “ –`, emoji); a stale tag is only dropped when every character fits the declared codepage;
- [x] safer audio choice (stems excluded, name preference, report when ambiguous),
  `#MP3` for video-only songs, and stem spelling fixes;
- [x] folders with non-UTF-8 names skipped, unreadable txts counted once and never treated
  as "broken", and leftover categories merged in `invalid/`;
- [x] cleanup skips songs whose txt has a decode Problem and keeps the cover/background
  fixtxt would use; reports and the journal are fsynced.

Next slices:
- Signal handling: `signal.NotifyContext` in main, a ctx in `Config`, `exec.CommandContext`
  for ffmpeg, and removing the temp file on cancel.
- Parse each song's txt **once** into a `Song` struct (tags with their original case and
  order, note lines, the chosen txt file) that all modules share. This replaces substring
  reference checks. (Dry-run already applies earlier modules' planned txt changes through
  `ops.ReadFile`; a shared `Song` would read that planned content too.) The parser exists
  (`songtxt`, used by check); fixtxt, cleanup and the media modules don't use it yet.
- CLI: subcommands (`fixtxt`, `cleanup`, `video`, `audio`, `dedupe`, `check`, `organize`) or
  at least opt-in destructive modules, progress output (n/total, ETA), and an undo journal
  for moves and txt edits (not for re-encoded media, see "Design decision").
- Concurrency: worker pools for fixtxt and cleanup as well (fixaudio and fixvideos are done).
- Audio: loudness normalization (`loudnorm`, EBU R128) instead of peak normalization.
- CI: `go vet`, `staticcheck` and `go test -race`.

### 5. Final library structure: one folder per starting character

**Done** (2026-11) as the `organize` subcommand: any number of sources in priority tiers
(`-s a -s b:c`, `-priority`), songs always moved (keepers into `<library>/<bucket>/<Artist -
Title>`, losers to `<out>/duplicates/<label>/`, broken songs to `<out>/invalid/<label>/`),
`Bucket` (0-9, A–Z, #; `-ignore-the`), safe `FolderName`, collisions ` (2)`, the library as a
source for idempotent re-runs and re-bucketing, `-n` writing `organize_plan.json/.txt`, and a
journal with `-undo`. **Still open:** renaming the files inside song folders
(`Artist - Title.mp3`, with the txt updated in the same step), applying an edited plan
(`--plan` file as input: today a real run re-plans, which gives the same plan for unchanged
sources), merging into an existing song folder (never done: such keepers stay in place), and
undoing more than the journal knows (re-encoded media, see "Design decisions").

Original plan. Goal: one consolidated library sorted into a subfolder per starting
character, the way UltraStar Manager organizes songs:

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
