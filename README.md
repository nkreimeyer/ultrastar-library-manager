# karaoke-scripts

Tools for sanitizing an [UltraStar](https://usdx.eu/) karaoke song library: each song is a
folder containing one `.txt` (lyrics/notes + `#TAG:value` header), an audio file and
optionally a video, cover and background image.

The main tool is `songs-sanitizer`, a Go CLI in `src/main/songs-sanitizer/`. The `old/`
directory contains the previous Python/shell scripts (including folder-name based duplicate
detection) and is kept for reference only.

## Requirements

- Go 1.24+ to build
- `ffmpeg` and `ffprobe` on `PATH` (only for the video `-m` and audio `-a` modules and the
  media checks of `-k` and `organize`; without ffprobe, `-k` checks only the txt files and
  `organize` cannot compare video and audio quality)

## Build

```bash
cd src/main/songs-sanitizer
go build -o songs-sanitizer .
```

Fully statically linked Linux binary:

```bash
cd src/main/songs-sanitizer
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -tags netgo -ldflags '-w -extldflags "-static"' -o songs-sanitizer .
```

## Usage

```bash
songs-sanitizer -s <song-dir> [flags]
songs-sanitizer organize -s <dir> [-s ...] -l <library> [flags]   # see "Organizing the library"
```

| Flag | Default | Description |
|---|---|---|
| `-s <dir>` | – | **Required.** Folder containing the song folders (nested category folders are supported) |
| `-o <dir>` | `sanitizer-output/` next to `-s` (symlinks resolved) | Output folder for `invalid/`, `extra-files/`, `originals/` and reports. Must not be inside `-s` |
| `-n` | off | Dry run: log what would be changed and write the reports, but do not modify the library |
| `-t` | **on** | Text-file module (`fixtxt`) |
| `-c` | **on** | Cleanup module |
| `-k` | off | Check module: report broken and suspicious songs (read-only) |
| `-deep` | off | With `-k`: fully decode every referenced audio/video file with ffmpeg (slow) |
| `-move-broken` | off | With `-k`: move songs with errors to `invalid/` |
| `-m` | off | Video module (re-encoding) |
| `-a` | off | Audio module (stereo downmix, normalization) |
| `-v <level>` | `info` | Log level: `debug`, `info`, `warn`, `error` |

`-t` and `-c` are enabled by default. Bool flags are disabled with `=`, e.g. `-t=false`;
`-t false` is rejected because it would stop flag parsing. Modules run in the order
fixtxt → cleanup → check → video → audio. Exit codes: `0` success, `1` some operations
failed (see the log and the summary line), `2` invalid command line (also `-deep` or
`-move-broken` without `-k`). Broken songs found by `-k` are findings, not failed
operations: they do not change the exit code.

> **The tool modifies your library in place.** Run it with `-n` first and keep a backup.
> The text and cleanup modules never delete anything: removed files and broken songs are
> moved into the output folder, mirroring the library's folder structure, so they can be
> restored. The audio module (`-a`) keeps a copy of every file it re-encodes in
> `originals/`. The video module (`-m`) replaces videos in place without a backup (keeping
> them would make the library very large). This includes conversions that change the file
> name (e.g. `.avi` → `.mp4`): the original is deleted once the song `.txt` points to the
> new file.

In dry-run mode every module sees the song `.txt` files as the earlier modules would
have left them: a txt that fixtxt would rewrite is read with its planned content (e.g.
cleanup keeps the audio file a fixed `#MP3` tag references, and check does not report a
swapped `#MP3`/`#VIDEO` or a missing `#ARTIST` that fixtxt would fix), and files and
folders an earlier module would move away (a duplicate song txt, a broken song) are
skipped. So the plan matches what a real run does. Not simulated: media changes planned
by the video and audio modules are only listed and counted, not applied in the dry run.
This includes the `#VIDEO` update of a conversion that changes the file name
(`.avi` → `.mp4`). No later module depends on them today: the audio module, the only one
after the video module, handles audio files and the `#VOCALS`/`#INSTRUMENTAL` tags only.

### What the modules do

- **fixtxt** – sanitizes the song `.txt` files
  - converts text files to UTF-8. Tried in order: UTF-16 with BOM, strict UTF-8, the codec
    named by `#ENCODING` (CP1250–CP1258, ISO-8859-x, KOI8; "Latin-1" is read as CP1252),
    CP1250 if `#LANGUAGE` names a Central European language, otherwise CP1252. A stale
    `#ENCODING` tag is removed. **When in doubt the file is left untouched** and listed in
    `encoding_issues.txt`: bytes undefined in the codec, mixed encodings or mojibake, an
    unsupported `#ENCODING`, CP1250 text without a tag, txts already containing `�`, and
    folders with media file names that are not valid UTF-8 (cleanup leaves those songs alone too)
  - if a folder has several song `.txt` files, keeps the most recently modified one and moves the others to `extra-files/`
  - fixes or adds `#MP3`/`#AUDIO`, `#VIDEO`, `#COVER` and `#BACKGROUND` tags so they point
    to existing files of the right kind, with the exact on-disk spelling. Cover and
    background are told apart by file name hints (`[CO]`, `[BG]`, the words `cover`,
    `background`, `bg`, unless they are part of the song's name) and then by shape (square
    within 5 % = cover); JPEG, PNG, GIF, BMP and WebP headers are read
  - removes repeated header tags, keeping the last one (the one UltraStar uses)
  - fills a missing or empty `#ARTIST`/`#TITLE` from the folder name when it is obvious:
    the folder is named `Artist - Title` (separated by ` - ` with spaces; `AC-DC` or an en
    dash do not count), and a tag that is still present must match its side of the name
    (ignoring case, extra spaces, trailing dots, typographic apostrophes, characters file
    names cannot hold such as `?` or `/` in `AC/DC` vs. `AC-DC`, and trailing
    `[DUET]`/`(Live)` qualifiers on the folder). Several possible splits, a reversed
    `Title - Artist` name, no match, an artist that would contain ` - ` (a prefix such as
    `SingStar - Artist - Song`) or an artist of only digits (a track number like
    `01 - Song`) fill nothing. Existing values are never changed; a tag written as
    `#ARTIST :x` or indented counts as present. The filled lines go to the top of the
    header, after a leading `#VERSION`. Every fill is added to `filled_tags.txt` for review;
    tags that stay missing are listed in `missing_files.txt` with the reason
  - moves songs without an UltraStar song `.txt` or without any audio/video to `invalid/`
  - writes `missing_files.txt` (songs without cover / background, ambiguous `#MP3`, a
    missing `#ARTIST`/`#TITLE` that cannot be filled)
- **cleanup**
  - moves files not referenced by the song's `.txt` to `extra-files/`
  - moves folders with at most one file to `invalid/`
- **check** (`-k`) – reports broken and suspicious songs in `check_report.json` and
  `check_report.txt`. A song with at least one *error* is **broken** (it will not load or
  play correctly), one with only *warnings* is **suspicious**. Read-only unless
  `-move-broken` is given, which moves broken songs to `invalid/`. Songs whose txt has an
  encoding problem are never moved, since their file references may only look broken.
  - txt: missing `#TITLE`/`#ARTIST`/`#MP3` (or `#AUDIO`), invalid `#BPM`/`#GAP`
    (decimal comma allowed), malformed note lines, no notes (fewer than 10 is a warning),
    no `E`, beats going backwards (error) or overlapping notes (warning), inconsistent duet
    sections, repeated tags (the last one counts, as in UltraStar). A missing
    `#ARTIST`/`#TITLE` that fixtxt can fill from the folder name says so in the message
    (with fixtxt enabled, check already sees the filled tag, in a dry run too)
  - referenced files: missing or zero-length files (an error for the audio source, a
    warning for video, cover, background and stems), unreadable media, an audio source
    without an audio stream, unreadable cover/background images
  - duration: notes ending more than 1 s after the end of the audio (wrong `#BPM`/`#GAP` or
    truncated audio), lyrics ending before half of the song with more than 30 s left, a
    video ending more than 10 s before the audio (honouring `#VIDEOGAP`), and invalid
    `#START`/`#END`. Notes after `#END` are only a warning. mp3 lengths that ffprobe can only
    estimate are measured by reading the packets first
  - `-deep`: a full decode of every referenced file. A file ffmpeg cannot decode is an
    error for the audio source; decode errors in the middle of a file (truncated or
    damaged) are warnings
- **video** (`-m`) – re-encodes videos to H.264 MP4 when potentially beneficial
  - re-encodes old or too new codecs and high-bitrate videos, downscales videos wider than
    1920 or taller than 1080 px
  - removes the video's audio track if `#MP3`/`#AUDIO` names a separate audio file
  - flags files ffmpeg cannot read in `potentiallyCorruptedFiles.txt`
  - every video it writes or checks is tagged (`SANITIZER_VIDEO`) and skipped on later
    runs; containers that cannot hold the tag (AVI, MPEG) are recorded in `media-journal.txt`
- **audio** (`-a`) – downmixes multichannel audio to stereo and peak-normalizes to -3 dB
  (true sample peak), keeping codec, bitrate, tags and embedded cover art. The original is
  copied to `originals/` first. Normalized files are tagged (`SANITIZER_NORMALIZED`, or
  recorded in `media-journal.txt` for raw AAC/WAV) and not normalized again on later runs.
  `#VOCALS`/`#INSTRUMENTAL` stems are left alone so their mix balance is kept

### Output folder layout

```
<out>/.songs-sanitizer-output                marker, the folder is never scanned as a library
<out>/invalid/<relative song path>/          broken song folders
<out>/extra-files/<relative song path>/      files removed from song folders
<out>/originals/<relative song path>/        originals of re-encoded audio files (-a)
<out>/media-journal.txt                      media already processed that cannot carry a tag
<out>/missing_files.txt                      songs without cover / background, ambiguous #MP3,
                                             #ARTIST/#TITLE that cannot be filled (with the reason)
<out>/encoding_issues.txt                    txts left untouched or needing manual repair
<out>/filled_tags.txt                        #ARTIST/#TITLE values fixtxt took from the folder name;
                                             kept across runs, a dry run's planned fills are
                                             marked "dry run: not written yet" and replaced next run
<out>/potentiallyCorruptedFiles.txt          videos ffmpeg cannot read (-m)
<out>/check_report.json, check_report.txt    broken and suspicious songs with reasons (-k), regenerated each run
```

If a target already exists (e.g. from an earlier run), a suffix like ` (2)` is appended;
empty folders are merged into an existing target instead.

## Organizing the library (`organize`)

`songs-sanitizer organize` builds one consolidated library from any number of source
folders, sorted into one folder per starting character, and removes duplicates across the
sources:

```
<library>/
  0-9/   2 Unlimited - No Limit/
  A/     ABBA - Dancing Queen/
  …
  Z/
  #/     everything else (symbols, non-Latin scripts)
```

```bash
songs-sanitizer organize -s <dir>[:<dir>...] [-s ...] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-n] [-v level]
songs-sanitizer organize -undo [-o <out> | -l <library>] [-n] [-v level]
```

Songs are always **moved**, never copied or deleted: the best version of every song goes
into the library, the other versions to `<out>/duplicates/<source>/…`, broken songs to
`<out>/invalid/<source>/…` (mirroring the source's folder structure). Every move is
recorded in a journal and can be undone with `-undo`.

**Recommended workflow:**

1. Run the sanitizer on every source first (`songs-sanitizer -s <source> -n`, then without
   `-n`; add `-k` to see what check reports). fixtxt repairs encodings, file references and
   missing `#ARTIST`/`#TITLE` tags, so fixable songs are not sent to `invalid/` by organize.
2. `songs-sanitizer organize … -n` and review `organize_plan.txt` in the output folder: what
   goes where, which version of each duplicate is kept and why, which songs are invalid or
   left in place, and possible duplicates that were *not* merged.
3. Run the same command without `-n`. If something went wrong, `organize -undo` restores
   the previous state.

| Flag | Default | Description |
|---|---|---|
| `-s <dir>[:<dir>…]` | – | **Required** (unless `-undo`). Source folder(s). Repeat `-s` for every priority tier; one `-s` may list several folders separated by `:` (`;` on Windows) |
| `-l <dir>` | – | **Required** (unless `-undo -o`). The library to build. It may not exist yet, and it may also be given as a source |
| `-o <dir>` | `organize-output/` next to `-l` | Output folder for `duplicates/`, `invalid/`, the journal and the reports. Must not be inside the library or a source |
| `-priority` | off | The order of the `-s` flags counts: a working song from an earlier `-s` always beats one from a later `-s` |
| `-ignore-the` | off | Sort "The Beatles" under `B` (the folder name keeps "The") |
| `-deep` | off | Fully decode every referenced audio/video file while checking the songs (slow) |
| `-n` | off | Dry run: write the plan report only; nothing is moved and no journal entry is written |
| `-undo` | off | Undo the most recent organize run that was not undone yet |
| `-v <level>` | `info` | Log level |

**Tiers.** With three sources, one high quality and two low quality ones:

```bash
songs-sanitizer organize -s /m/hq -s /m/low1:/m/low2 -l /m/library -priority
```

A song from `hq` is always kept over the same song from `low1` or `low2`; between `low1`
and `low2` the better quality wins. Without `-priority` all sources are equal and quality
decides. A working version always beats a broken one: if the `hq` version is broken, it
goes to `invalid/` and the report names the version used instead.

**Which songs are the same.** Songs are compared by their `#ARTIST` and `#TITLE` tags, not
by folder names: case, accents of Latin and Greek letters (`Beyoncé` = `Beyonce`; in other
scripts such marks make different letters, so `Мой` ≠ `Мои`), invisible characters (soft
hyphens, zero-width spaces), punctuation, `&`/`and`, a leading "The" of the artist and
`feat.` parts are ignored, and so are qualifiers that do not change the song
(`(Remastered 2011)`, `(Official Video)`, `[HD]`, `(Karaoke)`, a year). Every other
qualifier keeps songs apart (`(Live)`, `(Acoustic)`, `(Remix)`, `(German Version)`, …),
also next to a `feat.` part (`Song (feat. X Live)` ≠ `Song`) or inside a duet qualifier
(`[Duet - Acoustic]` ≠ `[Duet]`). A duet (P1/P2 in the txt, or `[DUET]` in the title) is a
variant of the solo version, not a duplicate. When in doubt nothing is merged: songs that
only differ by such qualifiers, or by an `ft.` in the title (it may also be an
abbreviation, as in `Ft. Lauderdale`), are listed as *possible duplicates* for manual review.

**Which version is kept.** The criteria are applied in this order; at each step only the
versions with the best value stay in the running:

1. with `-priority`: the earlier `-s` tier;
2. fewer warnings about the txt, the timing or the audio (problems with video, cover,
   background, `#VIDEOGAP` or stems do not count here);
3. has a readable video; 4. higher video height (e.g. 1080p over 720p);
5. an audio bitrate within 10 % of the best version's (unknown loses);
6. has a cover; 7. has a background; 8. the txt has `#VERSION`;
9. otherwise the earlier source (in the order given), then the path.

The report gives the deciding reason for every duplicate, e.g. `lower priority tier`,
`no video` or `lower audio bitrate (128 vs 320 kbit/s)`. Without `ffprobe` on `PATH` the
video height and the bitrate are unknown (the report says so): a version whose `#VIDEO`
names an existing file still beats one without a video, and the other criteria decide.

**Folder names** are `Artist - Title` from the tags, made safe for Windows, macOS and
Linux (`AC/DC` → `AC-DC`, `?*<>` removed, `"` → `'`, trailing dots removed, reserved names
like `CON` changed, at most 180 bytes). A duet without a duet qualifier in its title gets
` [DUET]`. Two different songs with the same name (ignoring case) get ` (2)`, ` (3)`, ….
The bucket is the first letter or digit of `#ARTIST` with accents removed (`Ä` → `A`,
`Ø` → `O`); leading punctuation is skipped (`'Til Tuesday` → `T`, `.38 Special` → `0-9`).

**Left in place** (reported with the reason, never moved): songs whose txt has an encoding
problem (fix them with fixtxt first), unreadable folders, songs whose target folder
already exists in the library but is not part of this run, and songs whose bucket folder
cannot take them: a file, an unreadable folder (counted as a failure), or a song folder
itself (for example an old song folder named `A` with a txt or media files in it; a song
moved into it would be hidden inside that song). To deduplicate against an
existing library, give the library as a source too:

```bash
songs-sanitizer organize -s /m/library -s /m/new -l /m/library -priority
```

Its songs then compete like all others; songs already at their target stay where they are,
and songs in the wrong place are moved to the right bucket. With `-priority` the tier order
counts here too: in the example the library outranks `/m/new`, which suits a new source
of lower quality. For a better source put it first (`-s /m/new -s /m/library`), or leave
out `-priority` (or give both in one tier, `-s /m/library:/m/new`) to let quality decide. Running
`organize -s /m/library -l /m/library` on a finished library moves nothing. Empty folders
(for example category folders emptied by an earlier run) are ignored and left alone.

**Undo.** `organize -undo -o <out>` (or `-l <library>` for the default output folder)
reverts the most recent run that was not undone yet: every move in reverse order, and the
library folders the run created are removed again if they are empty. A move whose target
is gone or whose original place is taken again is skipped and counted as failed. Run
`-undo` again to revert the run before that.

**Output folder** (`organize-output/` next to the library by default):

```
<out>/duplicates/<source>/<relative path>/    versions that lost against a better one
<out>/invalid/<source>/<relative path>/       broken songs
<out>/organize_plan.json, organize_plan.txt   the plan (and, for real runs, the outcome of every move)
<out>/organize-journal.jsonl                  every completed move, used by -undo
```

`<source>` is the base name of the source folder (`low1`, …; ` (2)` is added if two
sources have the same name).

## Development

See [CLAUDE.md](CLAUDE.md) for architecture notes, known issues and the roadmap.

```bash
cd src/main/songs-sanitizer
go vet ./... && go test ./...
```
