# karaoke-scripts

Tools for sanitizing an [UltraStar](https://usdx.eu/) karaoke song library: each song is a
folder containing one `.txt` (lyrics/notes + `#TAG:value` header), an audio file and
optionally a video, cover and background image.

The main tool is `library-manager`, a Go CLI in `src/main/library-manager/` that runs on
**Linux, Windows and macOS** (one static binary per platform, no cgo). The `old/`
directory contains the previous Python/shell scripts (including folder-name based duplicate
detection) and is kept for reference only.

Prebuilt binaries for all three platforms are on the
[GitHub releases page](https://github.com/nkreimeyer/ultrastar-library-manager/releases); to build from source see "Development" at the end.

## Supported platforms

| | Linux | Windows | macOS |
|---|---|---|---|
| Release binary | `library-manager-linux-amd64` | `library-manager-windows-amd64.exe` | `library-manager-darwin-amd64` (Intel), `library-manager-darwin-arm64` (Apple Silicon) |
| Start the UI | run it or double-click (opens a terminal) | double-click the `.exe` | double-click (opens Terminal) |
| Install ffmpeg | `sudo apt install ffmpeg` / `sudo pacman -S ffmpeg` | `winget install ffmpeg`, or [step by step without the command line](docs/ffmpeg-windows.md) | `brew install ffmpeg` |
| `-s` with several folders | separated by `:` | separated by `;` | separated by `:` |

The release binaries are built for amd64 (Linux, Windows, macOS) and arm64 (macOS, for Apple
Silicon Macs). On other CPUs (e.g. ARM Linux or Windows on ARM) build it yourself, see "Build".
Path handling follows each file system: case-insensitive comparison on Windows and macOS, Unicode
normalization (NFC/NFD) on macOS, and folder names that are safe on all three systems. A library
can be moved between the platforms. The CI runs the tests on Linux only.

## Requirements

The release binaries need nothing but ffmpeg (optional for some features):

- `ffmpeg` and `ffprobe` 5.1 or newer on `PATH` (only for the video `-m` and audio `-a`
  modules and the media checks of `-k` and `organize`; `-m` and `-a` refuse to run with a
  missing or older version; without ffprobe, `-k` checks only the txt files and `organize`
  cannot compare video and audio quality, and with an older version they warn)

## Usage

```bash
library-manager                                                   # without arguments: the browser UI, see "Browser UI"
library-manager ui [-addr 127.0.0.1:0] [-no-browser] [-keep-running] [-v level]
library-manager -s <song-dir> [flags]
library-manager organize -s <dir> [-s ...] -l <library> [flags]   # see "Organizing the library"
library-manager dedupe -l <library> [flags]                       # see "Removing duplicates in place"
library-manager add -s <dir> [-s ...] -l <library> [flags]        # see "Adding songs to a library"
library-manager remove -l <library> -artist <name> | -song "Artist - Title" | -from <file> [flags]   # see "Removing songs or artists"
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
| `-no-cache` | off | Probe every media file again: neither read nor write the media cache (see "Media cache") |
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
The one file a dry run writes into the library is the hidden media cache (see below).

### Media cache

Probing the media files (ffprobe, ffmpeg measurements) is most of the time a check, a
dedupe or a tidy-up takes. The results are kept in a hidden file in the root of the library
(`-s`, or `-l` for organize, dedupe and add, and the library in the UI):
`.library-manager-cache.json.gz` (gzip-compressed JSON, grouped by song folder, with the
time of the last run). The next run probes only the files that changed: a file counts as
unchanged while its size and modification time are the same. Songs moved with their
modification times (organize's own moves, `mv`, `rsync -a`) keep their results. A new
ffmpeg/ffprobe version discards all results. Song `.txt` files are always read anew.
A failure that may be passing (ffprobe or ffmpeg killed, or a read error such as
`Input/output error` while a network share drops for a moment) is never kept: the next run
tries that file again. A file the tools really cannot read stays recorded until it changes.

The file is written in dry runs too, and it is never moved by cleanup (it is outside every
song folder). If a run is killed while it writes the file, a hidden
`.library-manager-cache.json.gz.<random>.tmp` can stay next to it; the next run that saves
the cache removes such files once they are an hour old. When organize or add created the
library folder, `-undo` removes it again together with the cache file. The cache is used
by the check task of the UI as well. It is safe to delete at any time: the next run probes
everything again and writes a new one. `-no-cache` (main command, organize, dedupe, add) neither reads nor writes
it. A library root that is itself a song folder gets no cache.

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
  - writes `missing_files.txt` (songs without cover / background, ambiguous `#MP3` or
    `#VIDEO`, a missing `#ARTIST`/`#TITLE` that cannot be filled). Of several videos, the
    one an existing `#VIDEO`, `#MP3` or `#AUDIO` names wins, then one named like the txt,
    the folder or `Artist - Title`; otherwise `#VIDEO` is left as it is
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
  - flags files ffmpeg cannot read in `potentiallyCorruptedFiles.txt`, and keeps the
    original (and lists it there) when the processed video comes out shorter or longer
    than the original, as with an AVI whose damaged section ffmpeg drops. Each run
    rebuilds the list: a video found readable, or gone, is dropped from it (a dry run
    leaves the file alone)
  - a conversion that changes the name (`.avi` → `.mp4`) is not started when the song's txt
    cannot be updated afterwards (no reference to the video, an encoding problem, not
    UTF-8: run the txt fix first); the log names the reason
  - symbolic links to videos are left alone (process the file they point to)
  - every video it writes or checks is tagged (`SANITIZER_VIDEO`) and skipped on later
    runs; containers that cannot hold the tag (AVI, MPEG) are recorded in `media-journal.txt`
- **audio** (`-a`) – downmixes multichannel audio to stereo and peak-normalizes to -3 dB
  (true sample peak), keeping codec, bitrate, tags and embedded cover art. The original is
  copied to `originals/` first. Normalized files are tagged (`SANITIZER_NORMALIZED`, or
  recorded in `media-journal.txt` for raw AAC/WAV) and not normalized again on later runs.
  `#VOCALS`/`#INSTRUMENTAL` stems are left alone so their mix balance is kept, and so are
  symbolic links to audio files

### Output folder layout

```
<out>/.library-manager-output                marker, the folder is never scanned as a library
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
<out>/potentiallyCorruptedFiles.txt          videos ffmpeg cannot read or that change length when processed (-m)
<out>/check_report.json, check_report.txt    broken and suspicious songs with reasons (-k), regenerated each run
```

If a target already exists (e.g. from an earlier run), a suffix like ` (2)` is appended;
empty folders are merged into an existing target instead.

## Browser UI (`ui`)

Start `library-manager` without arguments (on Windows: double-click the `.exe`), or run
`library-manager ui`. It starts a small web server on `127.0.0.1` and opens the browser. Started
without arguments from a file manager (no terminal, but a desktop), the program opens a terminal
window for itself (Linux: the first of x-terminal-emulator, gnome-terminal, konsole, xfce4-terminal,
mate-terminal, kitty, alacritty, wezterm, xterm that is installed; macOS: Terminal; Windows
gives a double-clicked program its console window anyway; on macOS a downloaded binary may first
need `chmod +x` and, because it is unsigned, a right-click → Open or `xattr -d com.apple.quarantine
<file>`). Keep
the terminal window open while you use it. The UI stops by itself about 15 seconds after the
last browser tab is closed (a reload or a short network gap does not count) unless a job is
still running; `-keep-running` turns that off. Closing the window (or Ctrl-C) stops the UI and any
running job (a move or undo finishes the song it is on; on Windows closing the window
leaves only a few seconds for that, so press Stop or Ctrl-C and wait instead). The printed address signs one
browser in and then stops working, so a copy of it (browser history) lets no one else in;
if your browser lost its sign-in, restart library-manager for a new address. Several UIs
can run at once, each on its own port. Started with `nohup`, the UI keeps running after you
log out. A port given with `-addr` that is in use ends the
program with exit code 1.

The start page lists these tasks:

- **Tidy up my library**: `dedupe` together with the sanitizer's text, cleanup, audio and video
  steps; you choose the steps. Undo covers only the duplicates and broken songs set aside:
  leftover files and folders without a song stay in `extra-files/` and `invalid/` of the output
  folder, and fixed song texts, evened-out audio and converted videos stay as they are.
- **Add new songs to my library** (`add`) and **Build a new library from folders** (`organize`).
- **Check my songs for problems**: the check module as a dry run, shown as a report; it can then
  set the broken songs aside (not covered by undo).
- **Remove songs from my library** (`remove`, patterns typed in the page): check first, untick
  the songs to keep, then remove; undo puts them back.
- **Put back set-aside songs**: browse the duplicates, broken, removed songs and leftover files in
  the output folder and move chosen ones back (undo covers that too).
- **See what is in my library**: songs, size, video and picture counts, songs per folder, what is
  set aside.
- **Read the reports of earlier runs**: the `.txt`/`.json` reports of the output folder, read-only.
- **Free up space from audio backups**: the size of `originals/`, and deleting it after you
  confirm (the only thing the UI deletes; the library is never touched).

Tidy up, add and build work like this:

1. Choose what to do and pick the folders (a folder picker or a pasted path).
2. **Check first**: a dry run. Nothing in the library changes except the hidden media cache
   (see "Media cache"), so the move after it does not probe the media again.
3. Review the outcome: which songs move into the library, which duplicates and broken songs
   are set aside (and why), which stay where they are. **Leave this song alone** takes a
   song (or one version of it) out of the plan; the plan updates at once.
4. **Move**: the real run plans again and compares with the plan you reviewed. If anything
   changed in between (a new song, a moved folder), nothing is moved and the new plan is
   shown for another review.
5. **Undo this run** reverts the moves, like `<cmd> -undo`. An undo can be stopped; undo
   again to move back the rest.

**Easy** mode (the default) shows plain choices. **Advanced** adds the flags (`-priority`,
priority groups, `-ignore-the`, `-deep`, `-o`), a table of every song folder with its decision and media quality, the plan as JSON,
the earlier runs of the output folder and the live log.

The header shows whether `ffmpeg`/`ffprobe` are installed and new enough (5.1 or later);
without them the UI says what is skipped and how to install them.

Security: the server only listens on a loopback address. The start URL carries a one-time
key that sets a session cookie; requests without it, with another host name, or cross-site
are refused. Only one job runs at a time.

## Organizing the library (`organize`)

`library-manager organize` builds one consolidated library from any number of source
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
library-manager organize -s <dir>[:<dir>...] [-s ...] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-no-cache] [-n] [-v level]
library-manager organize -undo [-o <out> | -l <library>] [-n] [-v level]
```

Songs are always **moved**, never copied or deleted: the best version of every song goes
into the library, the other versions to `<out>/duplicates/<source>/…`, broken songs to
`<out>/invalid/<source>/…` (mirroring the source's folder structure). Every move is
recorded in a journal and can be undone with `-undo`.

**Recommended workflow:**

1. Run the sanitizer on every source first (`library-manager -s <source> -n`, then without
   `-n`; add `-k` to see what check reports). fixtxt repairs encodings, file references and
   missing `#ARTIST`/`#TITLE` tags, so fixable songs are not sent to `invalid/` by organize.
2. `library-manager organize … -n` and review `organize_plan.txt` in the output folder: what
   goes where, which version of each duplicate is kept and why, which songs are invalid or
   left in place, and possible duplicates and similar songs that were *not* merged.
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
| `-no-cache` | off | Probe every media file again: neither read nor write the media cache (see "Media cache") |
| `-undo` | off | Undo the most recent organize run that was not undone yet |
| `-v <level>` | `info` | Log level |

**Tiers.** With three sources, one high quality and two low quality ones:

```bash
library-manager organize -s /m/hq -s /m/low1:/m/low2 -l /m/library -priority
```

A song from `hq` is always kept over the same song from `low1` or `low2`; between `low1`
and `low2` the better quality wins. Without `-priority` all sources are equal and quality
decides. A working version always beats a broken one: if the `hq` version is broken, it
goes to `invalid/` and the report names the version used instead.

**Which songs are the same.** Songs are compared by their `#ARTIST` and `#TITLE` tags, not
by folder names: case, accents of Latin and Greek letters (`Beyoncé` = `Beyonce`; in other
scripts such marks make different letters, so `Мой` ≠ `Мои`), invisible characters (soft
hyphens, zero-width spaces), punctuation, spaces (`A-ha` = `Aha`, `Ring Ring` = `RingRing`),
`&`/`and`, HTML entities (`&amp;`), a leading "The" of the artist (also written as a trailing
`(The)` or `, The`: `Cranberries (The)` = `The Cranberries`) and `feat.` parts are ignored, and
so are qualifiers that do not change the song (`(Remastered 2011)`, `(Official Video)`, `[HD]`,
`(Karaoke)`, `(Kar)`, `(PV)`, `(MV)`, a year). The names of a collaboration are compared as
a set: `Kate Bush & Peter Gabriel` = `Peter Gabriel and Kate Bush` = `Peter Gabriel x Kate
Bush` (also `+`, `with`, `vs.`). A title that repeats the artist before a dash loses it
(`James Blunt - James Blunt-Wise Men` = `James Blunt - Wise Men`). Every other
qualifier keeps songs apart (`(Live)`, `(Acoustic)`, `(Remix)`, `(German Version)`, …),
also next to a `feat.` part (`Song (feat. X Live)` ≠ `Song`) or inside a duet qualifier
(`[Duet - Acoustic]` ≠ `[Duet]`). A duet (P1/P2 in the txt, or `[DUET]` in the title) is a
variant of the solo version, not a duplicate: both go to the library, the duet as
`Artist - Title [DUET]` (unless its title already names the duet), and they are never listed
as possible duplicates of each other. When in doubt nothing is merged: songs that
only differ by such qualifiers, or by an `ft.` in the title (it may also be an
abbreviation, as in `Ft. Lauderdale`), are listed as *possible duplicates* for manual review
(duets only with duets, solo versions only with solo versions).

**Same notes, other tags.** Songs whose txts have the same notes are the same song as well,
whatever their tags say (`Unknown Artist - Track 03` with the notes of `ABBA - SOS`). For
every working song, the content of the txt is fingerprinted (SHA-256): `#BPM` and `B` lines,
and every note with its type, beat, length, pitch, syllable and singer. Copies still match
when they differ only in the other tags (`#GAP` too: the same chart synced to another audio
file), the line breaks, the order of the note lines, RELATIVE mode, the encoding or the line
endings. Everything else must match exactly: one changed syllable, pitch or beat makes a
different song. A song with fewer than 10 notes gets no fingerprint, and a duet and a solo
version are never joined this way either. Songs with the same fingerprint join the same
group as songs with the same tags (also transitively), the keeper is chosen as below and
names the folder, and the report says "the tags differ, but the notes, timing and lyrics
are identical to another version of this song" for a duplicate found only through its notes.
A broken song with the notes of a working one names it as the version used instead.

**Same chart, small edits.** Many copies are the same chart with a few changes: typos fixed in
the lyrics, the whole song moved by an octave, `#BPM` doubled with all beats doubled, every
beat shifted. Such versions are joined too, when all of these hold:

- both are duets or both solo versions;
- at least 90 % of their lyrics match (the share of common 4-word sequences; a line break
  ends a word, also when the next line starts without a space);
- the charts match: the same number of notes, each with the same singer, the same pitch
  relative to the first note (within an octave), and a start (from the first note) and length
  within 30 ms;
- the artists are similar: equal ignoring case, accents, spaces and punctuation, one name
  contained in the other (`Glee` / `Glee Cast`, `Disney's Aladdin` / `Aladdin`), or a typo
  (at most a quarter of the letters differ: `Elvis Presly`);
- the titles name the same versions: a `Live` or `Mix` version with the chart of the studio
  version is listed for review instead.

The report then says "the tags differ, but the chart (timing and melody, within 30 ms) and
at least 90 % of the lyrics match another version of this song".

**Similar songs (review).** Weaker evidence never merges anything. The report lists, in its
own section "Similar songs", pairs of kept songs with a similar artist whose lyrics are at
least 60 % alike or whose shorter lyrics are 80 % contained in the longer ones (a TV size or
radio edit), and pairs with the same chart, whatever the artist (a parody, a translation, a
cover that reuses the chart, an artist alias). Every pair of a possible-duplicates group
gets the same evidence line, or "only N % of the lyrics alike: probably different versions",
so a live or TV version is easy to tell from a real duplicate. Covers by other artists with
a chart of their own are not listed.

**Copies marked `_dupl<n>`.** A song whose folder name, song txt name, audio or video file
name, `#ARTIST` or `#TITLE` ends in `_dupl1`, `_dupl2`, … (any case) is a copy: the suffix
is ignored when songs are compared and named, and a marked copy always loses against an
unmarked version of the same song. A copy without an unmarked version is kept (by
organize and add under its name without the suffix; dedupe does not rename).

**Which version is kept.** The criteria are applied in this order; at each step only the
versions with the best value stay in the running:

1. not marked as a copy (`_dupl<n>`, see above);
2. with `-priority`: the earlier `-s` tier;
3. fewer warnings about the txt, the timing or the audio (problems with video, cover,
   background, `#VIDEOGAP` or stems do not count here);
4. has a readable video;
5. a video bitrate of at most 3 Mbit/s, the limit above which fixvideos (`-m`) re-encodes
   (unknown counts as within): a higher one only bloats the library;
6. higher video height (e.g. 1080p over 720p), counted up to 1080p, since fixvideos
   downscales larger videos (2160p is no better than 1080p);
7. an audio bitrate within 10 % of the best version's (unknown loses);
8. has a cover; 9. has a background; 10. the txt has `#VERSION`;
11. otherwise the earlier source (in the order given), then the path.

The report gives the deciding reason for every duplicate, e.g. `lower priority tier`,
`no video` or `lower audio bitrate (128 vs 320 kbit/s)`. Without `ffprobe` on `PATH` the
video height and the bitrates are unknown (the report says so): a version whose `#VIDEO`
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
library-manager organize -s /m/library -s /m/new -l /m/library -priority
```

Its songs then compete like all others; songs already at their target stay where they are,
and songs in the wrong place are moved to the right bucket. With `-priority` the tier order
counts here too: in the example the library outranks `/m/new`, which suits a new source
of lower quality. For a better source put it first (`-s /m/new -s /m/library`), or leave
out `-priority` (or give both in one tier, `-s /m/library:/m/new`) to let quality decide. Running
`organize -s /m/library -l /m/library` on a finished library moves nothing. Empty folders
(for example category folders emptied by an earlier run) are ignored and left alone.
To add new songs without re-bucketing the library, use `add` instead (see below).

**Undo.** `organize -undo -o <out>` (or `-l <library>` for the default output folder)
reverts the most recent run that was not undone yet: every move in reverse order, and the
library folders the run created are removed again if they are empty. A move whose target
is gone or whose original place is taken again is skipped and counted as failed. If a
move failed because something is in the way, the run is not marked as undone: clear the
way and run `-undo` again, which retries that run and skips what is back already (the
journal records every move back). A song
that is gone (moved or deleted by hand) does not hold up the undo. Once a run is
completely undone, `-undo` reverts the run before that.

If a better copy from a source cannot be moved into the library (for example because the
source cannot be written, or the run is stopped), the library's own copy, already moved
to `duplicates/`, is moved back, so the library never loses a song; the report shows it
as `[moved back]`.

**Output folder** (`organize-output/` next to the library by default):

```
<out>/duplicates/<source>/<relative path>/    versions that lost against a better one
<out>/invalid/<source>/<relative path>/       broken songs
<out>/organize_plan.json, organize_plan.txt   the plan (and, for real runs, the outcome of every move)
<out>/organize-journal.jsonl                  every completed move, used by -undo
```

`<source>` is the base name of the source folder (`low1`, …; ` (2)` is added if two
sources have the same name).

## Removing duplicates in place (`dedupe`)

```bash
library-manager dedupe -l <library> [-o <out>] [-deep] [-no-cache] [-n] [-v level]
library-manager dedupe -undo [-o <out> | -l <library>] [-n] [-v level]
```

`dedupe` removes the duplicates of one library without reorganizing it: songs are compared
and the keeper is chosen exactly as by organize (including `_dupl<n>` copies), but the
keeper stays where it is, whatever the folder layout. The other versions go to
`<out>/duplicates/<library name>/…`, broken songs that have a working version to
`<out>/invalid/<library name>/…`. Broken songs without a working version, songs with an
encoding problem and unreadable folders stay. Nothing is renamed or re-bucketed. The plan,
`-n`, the journal and `-undo` work as for organize (the output folder is the same
`organize-output/` by default, and `-undo` reverts the newest organize, dedupe or add run).

## Adding songs to a library (`add`)

```bash
library-manager add -s <dir>[:<dir>...] [-s ...] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-no-cache] [-n] [-v level]
library-manager add -undo [-o <out> | -l <library>] [-n] [-v level]
```

`add` moves the songs of the sources into the library, deduplicated among themselves and
against the library's songs (which are always compared; do not give the library as `-s`):

- a song that is new to the library goes to `<Bucket>/<Artist - Title>` as with organize;
- if the library already has the song, the better version wins by the criteria above (so
  txt warnings and a video bitrate above 3 Mbit/s count against a version): a better input
  replaces the library's copy (which goes to `duplicates/`), otherwise the input goes to
  `duplicates/`. On a tie the library's copy stays;
- the library's own songs are not moved otherwise (no re-bucketing), and a broken library
  song only goes to `invalid/` when a working input replaces it. Broken inputs go to
  `invalid/`;
- a new song never takes the name of a library song that stays: it gets ` (2)`, ` (3)`, ….

With `-priority` the order of the `-s` flags decides first between the inputs; the library
counts as part of the first `-s`. The library may not exist yet (then `add` is the same as
organize). `-n`, the report and `-undo` work as for organize.

## Removing songs or artists (`remove`)

```bash
library-manager remove -l <library> [-artist <name>]... [-song "Artist - Title"]... [-from <file>]... [-o <out>] [-n] [-v level]
library-manager remove -undo [-o <out> | -l <library>] [-n] [-v level]
```

`remove` moves the songs that match to `<out>/removed/<path in the library>` (`-o` defaults
to `organize-output/` next to the library). Nothing is deleted: `remove -undo` puts the
songs back, and once you are sure you can delete `removed/` yourself.

- `-artist "Elton John"` removes every song of the artist, also collaborations with it
  (`Elton John & Kiki Dee`, `Elton John x …`), but not songs where the artist is only
  featured (`George Michael feat. Elton John`).
- `-song "ABBA - SOS"` removes that song, the solo and the duet version, but not other
  versions such as `SOS (Live)` unless you name them. `-song "ABBA - SOS [DUET]"` removes only
  the duet.
- `-from list.txt` reads patterns from a file, one per line: `Artist - Title` is a song, a
  line without ` - ` an artist; `artist:` or `song:` at the start forces the kind
  (`artist: Queen - The Band`). Blank lines and lines starting with `#` are ignored.

All three can be repeated and combined. Tags are compared as the duplicate detection does:
case, accents, spaces, punctuation, a leading `The` (or `(The)`), `feat.` parts, noise
qualifiers and `_dupl<n>` marks do not matter, so `-artist "the cure"` also removes `Cure (The)`.
A missing `#ARTIST` or `#TITLE` is taken from an obvious `Artist - Title` folder name (the
same rule the sanitizer uses to fill it; the report marks such matches), so a library that was
not sanitized yet can be cleaned too. Songs without a readable song txt are never matched, and a matching song whose txt has an
encoding problem stays in place (its tags may be read wrongly; fix it with the sanitizer
first). Run with `-n` first: `remove_plan.txt` (and `remove_plan.json`) in the output folder
list every song that would go, the pattern that matched it, and the patterns that matched
nothing (usually a typo); a real run writes them for what it removed.

`remove` shares the journal with organize, dedupe and add, so `-undo` of any of them reverts
the most recent of these runs.

## Development

See [CLAUDE.md](CLAUDE.md) for architecture notes, known issues and the roadmap.

### Build

Requires Go 1.24+.

```bash
cd src/main/library-manager
go build -o library-manager .
```

Cross-compile for another platform (set `GOARCH=arm64` for ARM; `CGO_ENABLED=0` keeps it
free of C dependencies):

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o library-manager.exe .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o library-manager-darwin-arm64 .   # Apple Silicon
```

Fully statically linked Linux binary:

```bash
cd src/main/library-manager
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -tags netgo -ldflags '-w -extldflags "-static"' -o library-manager .
```

Prebuilt binaries: the CI (`.github/workflows/build.yml`) runs gofmt,
`go vet` and the tests on every push (pushes to `main` and pull requests). Only a tag
`v*` also builds the binaries (`library-manager-{linux,windows,darwin}-amd64` and
`library-manager-darwin-arm64`) and creates a release with them attached (with a `SHA256SUMS` file).

### Tests

```bash
cd src/main/library-manager
go vet ./... && go test ./...
```
