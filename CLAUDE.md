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
                              os.Args[1] == "organize"/"dedupe"/"add"/"remove"/"ui" dispatches to the subcommand;
                              no arguments at all starts the UI (double-click); main.version is set with -ldflags -X
  ui_cmd.go                   the ui subcommand: -addr (loopback only), -no-browser, -keep-running, -v; opens the browser, stops on a
                              signal or when the last browser page is gone (browserGrace, webui.Server.WatchClients)
  terminal.go                 no-argument start outside a terminal: opens a terminal window that runs the program again
                              (needsTerminal, terminalCommand; `env LIBRARY_MANAGER_IN_TERMINAL=1` marks the child;
                              never on Windows; tests stub launchTerminal in ui_cmd_test's init)
  organize_cmd.go             the organize, dedupe and add subcommands (organize.Mode): own FlagSet, -s tier lists, -undo, validation
  remove_cmd.go               the remove subcommand: -artist/-song/-from patterns, -undo
  cmd_common.go               the steps all commands share: usageError/leftoverError, setLogLevel (-v), subcommand
                              (FlagSet + -v: parse, undoFlag, checkUndoFolder), journaledRun (organize/dedupe/add/remove:
                              config or undo config, organize.Undo or PrepareOutDir + run, "<name> failed", summary line)
  main_test.go                flag validation (-deep/-move-broken need -k), module order, organize usage
  pipeline_test.go            fixtxt → cleanup (→ check) integration tests (real run, dry-run = real-run plan, idempotence)
  config/                     Config{SongDir, OutDir, DryRun}: path validation, output layout, marker file;
                              organize.go: OrganizeConfig{Sources (tier, label), Library, OutDir}; NewDedupe, NewAdd
  fsops/                      the ONLY place that modifies files: dry-run (planned writes/moves visible via ReadFile), atomic writes, moves, stats;
                              planned.go: Stats.Sub, PlannedWrites/PlannedMoves (a dry run's list, for the UI review);
                              MoveTo (exact target, ordered dry-run moves), AppendJournal, RemoveEmptyDir (undo),
                              RemoveStaleReportTemps (PrepareOutDir: own .<name>.<digits>.tmp report/journal temps in the
                              out dir, older than StaleCacheTemp, logged),
                              RemoveEmptyDirWithCache (undo of a created library root),
                              WriteJSONReport/MarshalReport (the one JSON report encoding), WriteCacheFile (the media
                              cache: atomic, also in a dry run, not in Stats, sweeps stale own temps), FailUnreadable (the
                              "unreadable:<path>" FailOnce key), SkipUnreadable ("Skipping <folder>: <err>")
  utils/decode.go             txt decoding (DecodeTxt): UTF-16/UTF-8/#ENCODING/CP1250/CP1252, "when in doubt, Problem"
  utils/txt.go                header tags, song-txt lookup, file references, FileTags/IsFileTag (the seven tags naming files)
  utils/folders.go            song folder discovery (GetAllSongFolders), IsNoiseEntry (hidden, @eaDir, planned dry-run
                              move: no song content)
  utils/utils.go              media type checks, non-UTF-8 media names
  utils/language.go           #LANGUAGE names -> codes: LanguageCodes (prefix table languagePrefixes, first match
                              wins, empty code = stop; whole words notLanguageNames); used by DecodeTxt's
                              CP1250 choice
  utils/fold.go               FoldString (NFC, case folding, marks dropped on Latin/Greek letters and non-letters,
                              letterFolds for ø æ œ ł đ ð þ ß ı ħ ŧ ŋ ĸ ɨ ƶ and hooked letters), IsApostrophe (' ` ’ ‘ ´ ʼ);
                              used by organize keys/Bucket and LanguageCodes
  utils/parallel.go           ForEachParallel: the one worker pool (Parallel{Workers, Progress, OnPanic}),
                              ctx stops new items, a panic is recovered per item
  utils/songname.go           TagsFromFolderName: obvious #ARTIST/#TITLE fills from "Artist - Title" folder names
  utils/image.go              image headers (JPEG/PNG/GIF via stdlib, own BMP/WebP readers), cover/background choice
  songtxt/                    UltraStar txt parser (tags, notes, breaks, duets, RELATIVE, B lines, BeatToMs); pure;
                              the shared rules: IsBodyLine (end of header), LineSpans (line numbering), IsDuet
  fixtxt/                     dedupe song txts, decode → UTF-8, fix the seven file tags (utils.FileTags), fill #ARTIST/#TITLE from the folder name;
                              RunWith(Options{Encoding, FileTags, FillTags, DuplicateTxts, MoveBroken, Ctx, Progress}) (Run = all on);
                              with Encoding off a txt that is not UTF-8 is never written (every write is UTF-8)
  cleanup/                    move unreferenced files to extra-files/, near-empty folders to invalid/;
                              RunWith(Options{LeftoverFiles, BrokenFolders, Ctx, Progress})
  check/                      broken-song detection: analyze.go (pure), probe.go (ffprobe/ffmpeg), report.go;
                              inspect.go: Inspector (one folder -> findings, broken, MediaSummary), used by Run and organize
  fixvideos/                  ffmpeg: re-encode to H.264 mp4, downscale to fit 1920x1080, strip audio, flag corrupt files (tagged, idempotent)
  fixaudio/                   ffmpeg: downmix to stereo, peak-normalize to -3 dB, originals kept (tagged, idempotent)
                              (fixaudio and fixvideos: RunWith(Options{Ctx, Progress, Cache}); a stop starts no further file,
                              a running encode finishes, a stopped probe is never a failure)
  journal/                    content-fingerprint journal for media formats that cannot carry a marker tag
  mediacache/                 the hidden scan cache (FileName .library-manager-cache.json.gz in the library root):
                              Open (never fails; nil = disabled), Lookup/Store (Stamp), Save (prune, WriteCacheFile),
                              Stats; probe.go: Probe (mediatools.Probe through the cache, kind "probe/1")
  mediatools/                 mediatools.go: Detect, Lookup, Require (ffmpeg/ffprobe on PATH, "-version" parsed: releases,
                              N-… and date git builds; >= 5.1); probe.go: Probe (one ffprobe JSON call -> ProbeInfo),
                              FirstStream/HasStream, HasTag (marker tags), VideoBitrate (the one bitrate rule);
                              output.go: OutputCap/CombinedOutput (head 16 KiB + tail 48 KiB of ffmpeg output);
                              failure.go: FileFailure/SystemError (may a tool result be cached?)
  webui/                      browser UI: presence.go (open pages = open /api/job/events streams; WatchClients), server.go (single-use token -> session cookie lm_session_<port>, CheckAddr,
                              Host/Origin/JSON checks, CSP, routes),
                              jobs.go (one job at a time, logrus hook, SSE /api/job/events), system.go (tools, folder lister),
                              organize.go (validate/analyse/exclude/apply/history/undo),
                              check.go (the Check task: a dry-run check.Run shown as a report; "set aside broken songs" re-runs it
                              with MoveBroken, not journaled so not covered by undo; the analysis counts as applied
                              only once the apply job changed something or finished: a stop before any change can be retried),
                              overview.go (the overview job: songs, size, video/picture counts, songs per top folder, what is set aside; names and sizes, and whether a song txt is there),
                              originals.go (size of <out>/originals/ and its deletion: the one place the UI deletes files that
                              are not a conversion's own original; confirmed in the page, whole folder only, only in a folder with the output marker
                              (`config.IsOutputDir`), library never touched),
                              reports.go (the report viewer: the .txt/.json files of the output folder, read-only, cut off at 2 MiB; journals excluded),
                              setaside.go (the restore browser: list/restore over organize.ListSetAside/Restore; the origin is the
                              journal's src, else the mirrored path for extra-files/ and invalid/; a restore is one journal run, so undo covers it;
                              a request's dryRun only plans: ops follows cfg.DryRun),
                              remove.go (the Remove task: validate/analyse/apply over organize.Remove, patterns as text; apply
                              re-plans as a dry run and stops if the removed songs differ (less the reviewer's "keep" list,
                              organize.RemoveOptions.Keep); undo is the shared organize undo;
                              the job's Ctx and Progress reach RemoveOptions, a stop ends the job cancelled),
                              tidy.go (the tidy-up options and stepRun: the steps of one job on one ops),
                              static/ (index.html, style.css, app.js: embedded, no build step)
  organize/                   organize/dedupe/add: name.go (Bucket, FolderName, NameKey), key.go (SongKey, LooseKey, StripCopySuffix),
                              content.go (ContentHash, union-find joining of groups by content),
                              remove.go (Pattern, ParsePatternFile, planRemove (pure), Remove, remove_plan.json/.txt),
                              similar.go (Profile, findSimilar, similarJoin, review evidence),
                              plan.go also BrokenRule (BrokenByMode, BrokenKeep, BrokenSetAside) and KeepDuplicates (dedupe only), Join (pairs of Refs
                              the reviewer treats as one song; Review.ReplanWith; the UI's "Same song" on a Similar pair);
                              setup.go (NewConfig per Mode, NewUndoConfig, NewOptions: shared by the CLI and the UI;
                              dedupe drops -priority/-ignore-the), quality.go (keeper order), plan.go (MakePlan, orderMoves; pure), organize.go (Run, Undo),
                              journal.go (organize-journal.jsonl), report.go (organize_plan.json/.txt;
                              writeReportFiles also writes remove_plan.*), compare.go (PlanDiff, ErrPlanChanged: the
                              Review hook's plan comparison), restore.go (ListSetAside, Restore, RestoreRef, SetAsideItem:
                              the set-aside listing and restore, journaled)
  internal/testutil/          test helpers shared by the packages' _test.go files only (fixtures, snapshots, ffmpeg
                              checks and media generators, fake tools, log capture, NotesTxt); TestNoNonTestImports enforces it
old/                          legacy Python/shell (duplicate detection by folder name, fuzzy txt compare);
                              old/repair/: the archived U+FFFD repair (repairtxt, CLI, UI), not built (see its README)
test-assets/                  local fixture library (gitignored, not in the repo)
```

Dependencies: `logrus` and `golang.org/x/text` (charmaps, UTF-16, Unicode normalization).
CI: `.github/workflows/build.yml` (pushes to main, tags `v*`, pull requests, manual runs) and
`.gitlab-ci.yml` (branch pushes, or the merge request pipeline while one is open; tags) run gofmt, vet
and `go test -race` (with ffmpeg); only tags `v*` also cross-build linux/windows/darwin amd64 with `CGO_ENABLED=0` and make a
release (GitHub with `SHA256SUMS`, GitLab with the tag's `CHANGELOG.md` section as notes; untagged runs do
not build). The GitLab
jobs carry the runner tag `docker` (they need the Docker executor: `image:` is ignored by a shell runner). Keep the
module free of cgo so this keeps working.
Runtime needs `ffmpeg` ≥ 5.1 and `ffprobe` on PATH for `-m` and `-a` (enforced: `mediatools.Require`
makes the module's `Run` fail, exit 1); `-k` and organize use what they find (an older version with a
warning and a report note).

## Build, run, test

```bash
cd src/main/library-manager
gofmt -l . ; go vet ./... && go test -race ./...   # ffmpeg tests skip if ffmpeg is missing
go build -o ../../../test-assets/mybin .
cd ../../../test-assets && ./mybin -s songs -n     # dry run; outputs go to test-assets/sanitizer-output/
```

Flags: `-s <songdir>` (required) · `-o <outdir>` (default `sanitizer-output/` next to the resolved `-s`) ·
`-n` dry run · `-t` fixtxt (default **on**) · `-c` cleanup (default **on**) · `-k` check
(`-deep`, `-move-broken` only with `-k`) · `-m` fixvideos · `-a` fixaudio · `-no-cache` · `-v <level>`.
Order is always fixtxt → cleanup → check → fixvideos → fixaudio. Bool
flags are turned off with `-t=false`; leftover arguments (`-t false`) are rejected. Exit codes:
0 ok, 1 some operation failed, 2 usage error. See README.md for user-facing details.

Subcommands: `organize -s <dir>[:<dir>…] [-s …] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-no-cache] [-n]`,
`dedupe -l <library> [-o <out>] [-deep] [-no-cache] [-n]` (in place: the library is the only source, keepers stay),
`add -s … -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-no-cache] [-n]` (the library is always the first
source of the first tier, library keepers stay, new keepers go to their bucket),
`remove -l <library> [-artist <name>]… [-song "Artist - Title"]… [-from <file>]… [-o <out>] [-n]` (matching songs
go to `<out>/removed/<rel>`; artist patterns match a subset of the collaboration names (`artistNames`), song
patterns the SongKey with or without the duet marker; missing tags come from `utils.TagsFromFolderName`;
encoding problems stay), and `<cmd> -undo [-o <out> | -l <library>] [-n]`
(one journal for all four). Each `-s` is one priority tier (folders in
it split with `filepath.SplitList`); `-o` defaults to `organize-output/` next to the library.
Same exit codes and leftover-argument rule.

### Rules for working on the code

- **Update `CHANGELOG.md` in every commit that changes something a user can see** (new or changed
  flags, subcommands, UI behaviour, output, fixed bugs). Add one short line under `## [Unreleased]` in
  the matching Keep a Changelog group (Added, Changed, Deprecated, Removed, Fixed, Security). Pure
  refactors, tests, CI and docs-only commits need no entry. A release moves `[Unreleased]` to a
  dated version heading.
- **All library modifications go through `fsops.Ops`** (`WriteFileAtomic`, `MoveInto`,
  `MoveTo`, `CopyInto`, `Rename`, `Replace`, `Remove`, `RemoveEmptyDir`, `RemoveEmptyDirWithCache`,
  `PlanReplace`, `WriteCacheFile`; in the output folder `WriteReport`, `WriteJSONReport`, `AppendJournal`).
  Never call `os.Rename/Remove/WriteFile/Create` on library paths. The only exception is removing
  your own `fsops.TempFileNear` file after a failure. The rule is about the library:
  `config.PrepareOutDir` creates the output folder and its marker file itself. This is what makes dry-run, atomic writes and the summary/exit code work.
- **Never delete.** Removed files go to `cfg.ExtraFilesDir()`, and broken folders to
  `cfg.InvalidDir()`, using `ops.MoveInto(src, dir, cfg.RelPath(...))` so the library structure
  is mirrored. Reports go to `ops.WriteReport(cfg.ReportPath(name), …)` with sorted content;
  JSON reports to `ops.WriteJSONReport(cfg.ReportPath(name), v)` (no HTML escaping, two-space
  indent, one trailing newline), never `json.Marshal` + `WriteReport`.
  The only exception is fixvideos (see "Design decisions"), which deletes the original of a
  conversion with `ops.Remove`, and only after the txt references the new file. organize
  moves only (`<out>/duplicates/<label>/`, `<out>/invalid/<label>/`, the library), journals
  every move, and its undo removes only the empty folders its run created.
- **Dry-run** must leave `cfg.SongDir` byte-identical. Probes (ffprobe, volumedetect) are
  fine. Planned media changes are recorded with `ops.PlanReplace`. The one exception is the
  media cache file in the library root (`mediacache.FileName`, written by
  `ops.WriteCacheFile`, which also removes stale temp files of its own interrupted writes);
  dry-run tests that run with a cache compare the library without it (and without the
  root folder's own mtime). Nothing else may use `WriteCacheFile`.
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
  mutant count as KILLED. Don't use `-D <ref>` either: the diff paths are relative to the repo
  root, not the module, so every mutant is SKIPPED; run whole packages and keep the survivors on
  changed lines (`git diff -U0`). Tokens in switch `case` expressions are always NOT COVERED (a
  coverage-profile artifact); check those by hand. Surviving mutants that are equivalent
  (unique sort keys, log-only arguments, buffer sizes) are expected.
- **Never use `go run .` against a library**, and never point the tool at the real library
  while developing. Use a copy in `test-assets/`.

## Architecture notes

- `config.New` resolves symlinks and refuses an out dir inside the song dir (`IsWithin`
  ignores case on darwin and windows, and Unicode normalization on darwin, like APFS/NTFS;
  GOOS is a parameter of `isWithin`). `PrepareOutDir`
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
- Header tags (`utils.HeaderTags`, `songtxt.Parse`, fixtxt's `headerLen`,
  `ReplaceFileReference`, the `#ENCODING` hint) are read up to the first body line
  (`songtxt.IsBodyLine`: note, `-`, `B`, `P`, `E`); junk lines inside the header are
  skipped, tags after it are ignored (`IssueTagAfterNotes`). `utils.IsSongTxt` also needs
  the first non-blank line to be a tag (a `License.txt` with `#TITLE:` further down is no
  song). A repeated tag resolves to its last header occurrence, as in UltraStar (also
  `songtxt.Song.Tag` and its accessors). A lone `\r` is a line break.
- `utils.DecodeTxt`: any NUL byte is a Problem, in UTF-16 a decoded U+0000 (BOM-less UTF-16 is
  still decoded, so the header stays readable). A UTF-32 BOM (checked before UTF-16's, which it
  starts with) is always a Problem; the text is decoded so the header stays readable. With an `#ENCODING` tag, only "clean" UTF-8 sequences count as
  mixed-encoding evidence, and in non-Latin codepages an isolated 2-byte sequence never does.
  A non-UTF-8 txt without `#ENCODING` is read as CP1250 when `utils.LanguageCodes` of its
  `#LANGUAGE` names pl, cs, sk, hu, hr, sl, ro or bs (`cp1250Languages`); the value is tried
  as it is (if valid UTF-8) and decoded as CP1250, and is split on `/ , & + ; | ( )` and
  blanks (`Polish & English`, `Polski (PL)` count). Names that only start like one of those
  languages have stop entries and keep CP1252 (Romani, Romansh, Romaji, Roma, Polka, Polari,
  Polynesian, Hunsrik, Rumba, …).
- fsops: a move across file systems (`isCrossDevice`: EXDEV, on Windows also
  ERROR_NOT_SAME_DEVICE; GOOS is a parameter so the Windows rule is tested on Linux) first
  checks that src can be renamed (fails cleanly if
  not), copies, then parks src under a hidden `.fsops-move-<pid>-<n>` name and deletes it.
  If that delete fails, the move still counts as done and the leftover is logged.
  `Replace` and `Rename` fsync the (ffmpeg-written) file before the rename and keep the
  original if that fails; the folder fsync after it is best effort (none on Windows).
  `AppendJournal` starts a new line after a crash-truncated last line. Collision names keep
  dotfiles intact (`.DS_Store (2)`). Temp names keep only the first 100 bytes of the file
  name (`tempBase`), so they stay within NAME_MAX. Real-run copies, renames and removals are
  logged at Info, `Replace` at Debug (fixaudio/fixvideos log their own result); writes are
  logged by the modules. In a dry run, the targets of `MoveInto`/`CopyInto` count as taken
  (a second move to the same name gets ` (2)`, as in a real run), and the merge decision
  sees folders emptied or put in place by planned moves (`plannedDirs`). `Rename` (only
  fixvideos' extension-changing conversion) is not `MoveTo`: it fsyncs the new file and the
  folder and, in a dry run, needs no existing src. fixaudio and fixvideos call `journal.Add`
  then `Save` per file: a failing Add is counted by the caller, a failing Save by ops.
- Concurrency: every module pool is `utils.ForEachParallel(ctx, items, utils.Parallel{Workers,
  Progress, OnPanic}, fn)`. Workers: fixtxt and cleanup 100 over folders (the size of the old
  batches, now a pool), fixaudio, check and organize (inspection, remove's tag reading)
  min(16, NumCPU) over folders, fixvideos 4 over video files. The same pool also runs the
  set-aside listing (`organize/restore.go`, organize's worker count, no ctx), the UI overview
  job (`webui/overview.go`, its own min(16, NumCPU)) and the cache prune (16). It never starts more goroutines than items; once ctx is
  done no item starts and it returns ctx.Err() (nil = every item ran); Progress calls are
  serialized. A panic in fn is recovered and logged with the item and stack, then OnPanic
  runs: modules log the song and call `ops.Fail()`, and the other songs still run, so fn must
  release locks with defer. Fatal runtime errors (concurrent map writes, OOM) are not
  recoverable. A song whose organize inspection panics stays in place as unreadable (in
  remove it never matches). Per-run state
  lives in a collector or processor struct with a mutex (no globals). `fsops` serializes
  target-name reservation.
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
  go to invalid/, working songs are grouped by `SongKey` (duet flag from `songtxt.IsDuet`, notes of two singers, or a `[DUET]`
  title), groups sharing a `contentKey` (`ContentHash` + duet flag) are joined by union-find
  (group key = smallest SongKey; a loser whose SongKey differs from the keeper's gets
  `joinedByContent` appended to its reason; a broken song without a SongKey match finds its
  replacement by content), then pairs from `findSimilar` that pass `similarJoin` are joined
  too (`joinedBySimilar`; the other pairs become `Plan.Similar` review entries with
  evidence), and the keeper is chosen by `chooseKeeper`: the criteria (not a `_dupl<n>` copy, tier
  with `-priority`, song warnings, video, video bitrate ≤ `fixvideos.BitrateThreshold`, height
  capped at `fixvideos.MaxHeight`, audio bitrate within 10 % of the group's best, cover,
  background, `#VERSION`, source order, path) are filters over the whole group, never pairwise
  comparisons, because the bitrate tolerance is not transitive. Each loser's reason is the
  filter that dropped it. Without ffprobe, `MediaSummary.HasVideo` only means `#VIDEO`
  names an existing, non-empty file (height and bitrate unknown = 0). `SongKey` folds with `utils.FoldString`: diacritics only on Latin/Greek letters (marks of
  other scripts make different letters), drops invisible format characters like FolderName,
  and never lets a `feat.` part or a duet qualifier swallow a version word (`Live`, `Remix`,
  …). Keys ignore spaces (words joined without them), treat a collaboration as a sorted
  set (`artistKey`: `&`/`and`/`+`/`x`/`with`/`vs`, not at the start or end of the name),
  drop a trailing `(The)`/`, The`, a title prefix equal to the artist before a dash
  (`cutArtistPrefix`) and HTML entities. Because the key ignores every non-letter, two
  different songs share a folder name only by truncation or by a dash split differently
  between artist and title (`A - Rock` / `Roll` vs `A` / `Rock - Roll`, used by the
  collision tests). `LooseKey` groups for "possible duplicates" are split by the duet flag: a duet and a
  solo version are both kept (the duet as `… [DUET]`) and never listed together. Target `<Bucket>/<FolderName>`; NameKey collisions of different songs get ` (2)`,
  ` (3)` from one run-wide claimed set (a number never takes another song's plain name;
  keepers already in place keep their number). A target taken by a non-candidate or a
  staying song, or a bucket that is a file, a song folder (song txt or media directly in
  it) or unreadable (`ops.FailOnce`), leaves the keeper in place. Apply: out-dir moves
  first (`MoveInto`), then library moves with `MoveTo` in `orderMoves` order (targets freed
  first, cycles and self-nesting through a hidden `.organize-tmp-*` name); right before each
  library move the bucket is checked again (an earlier move away of a song folder named like
  the bucket may have failed). The journal (`organize-journal.jsonl`: `run`, `mkdir`,
  `move`, `reverted`, `undone`) is appended and fsynced per entry, only in real runs; a journal write
  failure stops all further moves, and a song parked under the temporary name is moved back
  (or, if its place is taken, to its target) so it never stays hidden. After the library
  moves, `restoreLibraryLosers` moves library duplicates (in dedupe/add also broken library
  songs) whose keeper from another source did not arrive (failed, refused, stopped) back,
  journaled (report outcome `moved back`). `-undo` walks the
  newest not-undone run backwards; a run is only recorded `undone` if no retryable move
  failed (a move whose source and target are both gone does not block) and it was not
  stopped (`UndoOptions.Ctx`, checked before each entry but never while a song is at a
  `.organize-tmp-*` name). Each move back is journaled as `reverted` (entry number in the
  run, 1-based), and a retry skips those entries: the paths cannot tell, since both exist
  again after a swap is undone (`ops.Exists` is only the fallback for a lost record). Undo
  returns an error when moves back went unrecorded and the run is not recorded `undone` (a
  retry could repeat them). An undo stopped in the UI keeps its folders in the job result. A dry run executes the same steps through ops (MoveTo
  simulates the ordered moves), so its plan and move count match the real run
  (`TestRunDryRunMatchesRealRun`).
- check: `analyze` is a pure function over the parsed song and probed file info, so every
  rule is table-tested without ffmpeg. Findings are not `ops.Fail`s and don't change the
  exit code. Repeated tags resolve like UltraStar does (the last header occurrence wins;
  fixtxt's `removeDuplicateTags` also keeps the last and only looks at the header). For the
  seven file tags check (and so organize's `MediaSummary`) uses the last occurrence whose file
  exists, else the last (`fileRefs`); cleanup and fixtxt use the plain last occurrence. Songs
  whose txt has a decode Problem are reported but never moved by `-move-broken`.
  `mediaInfo.HasVideo` means a moving video stream (attached cover art does not count).
  `VideoBitrate` comes from `mediatools.VideoBitrate`, the rule check and fixvideos share
  (organize compares it with `fixvideos.BitrateThreshold`): the stream `bit_rate`, else its
  BPS statistics tag (a plain `BPS` in any case first, then `BPS-<lang>` tags in sorted
  order), else the container `bit_rate` of a file without an audio stream; 0 = unknown.
  fixvideos uses only the stream part (`mediatools.StreamBitrate`) and then measures packet
  sizes (an extra ffprobe run), never the container rate: it also counts CBR padding (a
  300 kbit/s video in an 8 Mbit/s MPEG-TS) and would trigger a re-encode that cannot be
  undone. So for a video-only file without stream rate check and fixvideos can differ. Media
  are probed once per file (`mediatools.Probe`, one ffprobe JSON call); separate runs remain
  only for measurements (packet sizes, astats, check's duration measuring and decode). Marker
  tags (`SANITIZER_*`): fixvideos counts them on the container or any stream
  (`ProbeInfo.HasTag`), fixaudio only on the container and the processed first audio stream
  (`mediatools.HasTagIn`). The `Inspector` takes a ctx (`NewInspector(ctx, …)`; organize
  passes `Options.Ctx`): a stop kills running probes and decodes (`exec.CommandContext`,
  `mediatools.WaitDelay`), `Probe` then returns `ctx.Err()`, and the inspection records no
  media result and no failure. Captured
  ffmpeg output that is only logged or searched keeps its first 16 KiB and last 48 KiB
  (`mediatools.CombinedOutput`; check quotes the first 64 KiB).
- webui: one job at a time, because the modules log through the global logrus logger and the
  job's log is what a hook collects while it runs (never log while holding `jobRunner.mu`). The
  analysis keeps `organize.Review` (candidates, plan, `Replan`); exclusions replan with
  `PlanOptions.Exclude` (`MakePlan` is pure; `Replan` reads the buckets it has not seen yet
  from the library, read-only). Apply runs `organize.Run` for real with a `Review` hook that
  compares the new plan with the reviewed one (`organize.PlanDiff`: action, target, keeper per
  `Ref`) and stops before the first move if they differ (the analysis then holds the new plan). An apply sets the analysis'
  `applying` flag before its job starts and the job clears it when it ends (also when it never
  starts); exclusions and joins answer 409 while `applying` or `applied` is set, checked before
  and after their `ReplanWith`.
  `organize.Options` has `Ctx` (checked before each inspection and move; a run stopped during the moves is journaled, a parked song is moved
  out of its temporary name) and `Progress`; so has `organize.RemoveOptions` (a stop while the tags
  are read moves nothing, moves not tried are `skipped` with "the run was stopped"). `fsops.Stats` has JSON names (the UI reads
  them). The start URL's token works once (a signed-in browser opening it again is only
  redirected); the cookie is `lm_session_<port>`, as browsers share cookies across ports.
  Only the newest analysis of each kind is kept. Every job handler starts its job with
  `Server.startJob` (202 with the job, 409 while one runs, 503 once `Server.Close` closed the runner: a request
  still handled during shutdown starts nothing); a dedupe ignores the request's
  priority/ignoreThe (the dedupe CLI has no such flags). Event streams send `id: <job>/<next line>`
  with each log event and resume from `Last-Event-ID`; `Server.Close` ends them (before
  `http.Server.Shutdown`; each sends the final job state first). `ui` stops on SIGINT, SIGTERM
  and SIGHUP, unless the signal was ignored at the start (`nohup`; `stopSignals`); `-addr` is checked
  before listening (exit 2), a listen failure exits 1. Tests: `webui/*_test.go` run the real server with `httptest` against temp libraries;
  `ui_cmd_test.go` stubs `openBrowser` in `init` (no test may open a browser); `stopSignals`
  reads `signal.Ignored` through `signalIgnored`, because a test's `signal.Ignore` stays
  visible there after `signal.Reset`. `runSafely`
  turns a panic in the job goroutine into a job error, and a panic in a module's pool counts
  in the job's `Stats.Failed`; a panic in any other goroutine still ends the program.
- mediacache: callers open one cache per run at the library root and save it at the end,
  also after a dry run, a failed module or a stop (the CLI only with `-k`/`-m`/`-a`, at
  `-s`; organize/dedupe/add at `-l`, not for `-undo` and `remove`; the UI's `stepRun` for
  the whole job if it has a song or media step, and both check jobs at the library);
  `-no-cache` opens none. A nil `*Cache`
  disables it everywhere. A file is unchanged if (path, size, mtime ns) equal the stored
  ones; a path miss (or a stale entry) looks up the secondary index (base name, size, mtime
  ns): a hit is re-keyed to the new path (copied if the old file is still there), so
  organize's moves into buckets keep their results. Store needs a fresh stat equal to the
  Stamp taken before the work and an mtime at least `RacyWindow` (2 s) old (git's racy-clean
  rule; FAT has 2 s); tests age media with `os.Chtimes`. The header holds the ffprobe and
  ffmpeg version strings; other versions drop every entry (and a run that then stores
  nothing does not overwrite the file, so a run without ffprobe on PATH keeps it; a done ctx
  at Open disables the cache for the same reason). Kinds carry a version (`probe/1`,
  `check.media/1` (probe result incl. measured duration, or the error message),
  `check.decode/1` (`-deep`), `fixaudio.peak/1`, `fixvideos.packets/1`): a change in how a
  result is computed bumps the kind. `mediacache.Probe` caches successes (with `Log`) and
  `*mediatools.ProbeError` (rebuilt with a plain `Err` of the same message); never
  `ErrNotRun`, ctx errors, parse errors, tool failures or stopped runs. A failure is only
  cached if it is about the file (`mediatools.FileFailure`: a normal non-zero exit, not a
  signal such as the OOM killer, and no system error in the output); a result whose tool
  output names a system error (`mediatools.SystemError`: `Input/output error`, a dropped
  connection, `Permission denied`, out of memory, …; ffprobe may exit 0 with partial
  results) is never cached, in every kind. Otherwise a NAS hiccup during one run would
  make a good file "unreadable" until it changes. fixvideos never
  uses the cache for its temp files or `sameVideoDuration` (the final check before a
  replace). Save prunes entries not seen in this run whose file is gone (a folder that
  cannot be listed keeps them; files seen in this run are kept even if gone, for the
  secondary lookup), and skips a root that does not exist. The prune lists the folders
  with unseen entries in parallel (16) outside the lock: after an `-a`-only run that is
  nearly every folder (its video). A folder renamed only in case (the old spelling still
  resolves on NTFS/APFS) is a move, not a copy: Lookup and prune compare the paths
  case/NFC-insensitively plus `os.SameFile` (tested with hard links on Linux). The file is
  rewritten on every Save (it records when the library was last seen).
  `ops.WriteCacheFile` first removes its own temp files older than `fsops.StaleCacheTemp`
  (1 h; left by a kill during the write, also in a dry run: they are in the root, which
  no cleanup visits). organize's undo removes a folder its run created that holds only
  the cache file (`ops.RemoveEmptyDirWithCache`: the library root a run created and saved
  the cache into). Open returns nil for a root that
  is itself a song folder (cleanup would move the hidden file). Save errors are warnings,
  never `ops.Fail`s. Txts are never cached.
- webui tidy-up ("Tidy up my library" = dedupe with `organizeRequest.Tidy`): the options page sends
  `tidyOptions`; `stepRun` runs fixtxt → cleanup → organize (dedupe; `Broken` = SetAside or Keep; `Folders` = fixtxt's MoveBroken and cleanup's BrokenFolders, apart from `Broken`,
  `KeepDuplicates` = !dedupe) → fixvideos → fixaudio on ONE ops, the modules with a `config.Config`
  on the organize out dir (one output folder). The check (dry run) skips the media steps (their dry
  runs decode every file) and lists the text and cleanup steps' planned writes/moves
  (`plannedChanges`, taken before the song step plans its moves). Apply runs the text and
  cleanup steps for real, then the song step with the PlanDiff hook; a failing song step (also a
  changed plan) ends the job before the media steps. Job kinds are `tidy-analyse`/`tidy-apply`.
  Only the song step is journaled: undo does not cover fixtxt/cleanup moves, txt rewrites or
  media. The media journal is `<out>/media-journal.txt`, so the CLI's `sanitizer-output/`
  journal is not shared with the UI's `organize-output/`.
- organize: when a keeper from another source cannot reach the library, the library's own
  copies of that song stay in place (not moved to duplicates/). The report's duplicate and
  invalid `Dest` is the actual target (with ` (2)`), also in dry runs. Moves a stopped run or
  a journal failure never tried are `skipped` with an `outcomeNote` ("the run was stopped",
  "the journal could not be written"). The fallback move of a parked song after a stop is
  journaled (unless the journal failed). `PlanOptions.LibrarySource` (set by `Run` when a
  `-s` is the library) tells `MakePlan` the library is a source even when it has no songs.
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
  (`SANITIZER_NORMALIZED`, `SANITIZER_VIDEO`). Formats that cannot hold one, and files
  whose lossless tagging remux failed (fixvideos' `markChecked`), go into the
  content-fingerprint journal (`journal`, `<out>/media-journal.txt`), which survives renames
  and moves (e.g. the A–Z reorganization). `.m4a` files are journaled, not tagged: writing
  the tag needs `-movflags +use_metadata_tags`, which drops the cover and the iTunes tags.
- fixvideos re-encodes always output 8-bit yuv420p with even dimensions. A conversion
  whose `.mp4` target already exists is skipped before encoding, and so is one whose txt
  cannot follow it (`utils.FileReferenceReplacement`: no reference, a decode Problem, not
  UTF-8). `utils.ReplaceFileReference` refuses txts with a decode Problem (`ErrTxtProblem`)
  and writes the new name NFC. A result whose first
  video stream is more than 0.5 s shorter or longer than the original's
  (`sameVideoDuration`: stream duration, else Matroska `DURATION` tag, else the container
  if the audio is kept) is discarded and the original listed in
  `potentiallyCorruptedFiles.txt`: ffmpeg exits 0 on an AVI with a damaged section and
  drops it, so the video would run ahead of the song with no original left. Such a file is
  encoded (and discarded) again on every run, like unreadable input. Unreadable input is
  exit 183, or exit 1 with "Invalid data found" (ffmpeg 5.1/6.0). Each real run rebuilds
  `potentiallyCorruptedFiles.txt`: an old entry is dropped when the run found the video
  readable or the file is gone; a dry run never writes it and only logs what it would drop.
- Symlinked media count as media (discovery, `collectVideos`), but fixaudio and fixvideos
  skip them with a warning: `Replace` would turn the link into a regular copy.

Still open (roadmap items):

- **No signal handling in the CLI.** Only `ui` stops cleanly (`signal.NotifyContext` over
  `stopSignals`; the job's ctx reaches the modules' `Options.Ctx`). The module flags and
  organize/dedupe/add/remove pass no ctx, so Ctrl-C kills the process: during `-m`/`-a` it
  leaves a hidden `.*.sanitizer-*` temp file (ignored as media, moved to `extra-files/` by the
  next cleanup run, the original untouched); during organize a song may stay parked under
  `.organize-tmp-*` until `-undo`. Even with a ctx, encodes, tagging remuxes and packet
  measurements use `exec.Command` and run to the end (only probes and check's decodes use
  `exec.CommandContext`).
- **Dry-run simulates txts and moves, not media.** Later modules see planned txt writes
  (`ops.ReadFile`) and planned moves away (`ops.WouldBeMoved`, planned merge targets), so
  the fixtxt → cleanup → check plan matches a real run (`TestPipelineDryRunMatchesRealRun`).
  Not simulated: media changes planned by fixvideos/fixaudio (`PlanReplace` only logs and
  counts them, and the `#VIDEO` rewrite of an extension-changing conversion is not recorded
  as a planned txt write); no later module depends on them today (fixaudio, the only module
  after fixvideos, reads only audio files and `#VOCALS`/`#INSTRUMENTAL`). Also not
  simulated: renames *into* a path (planned `MoveInto`/`CopyInto`/`MoveTo` targets are), and new files from planned writes in directory
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
  so `#ARTIST :x` counts as present. check names the fill in its message (not for a symlinked
  txt, which fixtxt never rewrites) (seen only with
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
similarity check 195, plus the key fixes 259 (no false merge found in review). **Still open:**
re-timed charts (another tempo) are only listed, never joined; artist aliases (`30`/`Thirty Seconds
to Mars`, `CCR`), placeholder artists, U+FFFD as a wildcard, German `ae`/`ä` spellings; audio fingerprints, and choosing between several song txts *inside one folder* by content.

Inside one folder, fixtxt still keeps only the newest song txt (`DuplicateTxts`, by mtime, no
content comparison) and moves the others to `extra-files/`; a content-based choice there should
keep only a txt that parses as UltraStar, treat duet/alternate txts as variants and never move
anything if no candidate qualifies. (The original plan, a Go port of `old/`'s folder-name
detection, is done as organize/dedupe above.)

### 2. Fix encoding checks and broken umlauts (ä ö ü ß …)

**Done in robustness slice 1:**
- [x] strict decoding (UTF-16 BOM → strict UTF-8 → CP1252), with ambiguous files left
  untouched and listed in `encoding_issues.txt`;
- [x] BOM and line-ending normalization, stale `#ENCODING` dropped, writes only when content
  changed;
- [x] NFC-insensitive file references;
- [x] non-UTF-8 file names reported.

**Still open:** mojibake repair. The `U+FFFD` repair was **done** (2026-11) as the `repair`
subcommand, archived in `old/repair/` on 2026-11-21 (`repairtxt`, tiers 1 and 3 below plus the library corpus, bigram context and
a char n-gram model, reviewed spellings with `-words`; no LLM pass). Still open there:
UTF-8 sequences lost as a whole (several U+FFFD for one character; stretched singing such as
`M���nner` = `Määänner` is one per character and works), non-Latin scripts (Korean CP949),
German `ae`/`ä` spellings as evidence (`Eine Maeh` → `Mäh`), and Dutch `ë` vs German `ß`
style confusions of the spelling model in small corpora.

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
  `’ “ –`, emoji); a stale tag is only dropped when every character fits the declared codepage
  (U+FFFD, a lost character, does not count against it);
- [x] safer audio choice (stems excluded, name preference, report when ambiguous),
  `#MP3` for video-only songs, and stem spelling fixes;
- [x] folders with non-UTF-8 names skipped, unreadable txts counted once and never treated
  as "broken", and leftover categories merged in `invalid/`;
- [x] cleanup skips songs whose txt has a decode Problem and keeps the cover/background
  fixtxt would use; reports and the journal are fsynced.

Next slices:
- Signal handling in the CLI: `signal.NotifyContext` in main and the subcommands, passed as
  the modules' and organize's `Options.Ctx` (the `ui` subcommand and the module contexts
  exist); `exec.CommandContext` for the encodes, and removing the temp file on cancel.
- Parse each song's txt **once** into a `Song` struct (tags with their original case and
  order, note lines, the chosen txt file) that all modules share. This replaces substring
  reference checks. (Dry-run already applies earlier modules' planned txt changes through
  `ops.ReadFile`; a shared `Song` would read that planned content too.) The parser exists
  (`songtxt`, used by check); fixtxt, cleanup and the media modules don't use it yet.
- CLI: subcommands (`fixtxt`, `cleanup`, `video`, `audio`, `dedupe`, `check`, `organize`) or
  at least opt-in destructive modules, progress output (n/total, ETA), and an undo journal
  for moves and txt edits (not for re-encoded media, see "Design decision").
- Audio: loudness normalization (`loudnorm`, EBU R128) instead of peak normalization.
- CI: `staticcheck` (`go vet` and `go test -race` run already).

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
  optional, and the txt must be updated in the same step (`utils.ReplaceFileReference`).
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
