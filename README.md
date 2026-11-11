# karaoke-scripts

Tools for sanitizing an [UltraStar](https://usdx.eu/) karaoke song library: each song is a
folder containing one `.txt` (lyrics/notes + `#TAG:value` header), an audio file and
optionally a video, cover and background image.

The main tool is `songs-sanitizer`, a Go CLI in `src/main/songs-sanitizer/`. The `old/`
directory contains the previous Python/shell scripts (including folder-name based duplicate
detection) and is kept for reference only.

## Requirements

- Go 1.24+ to build
- `ffmpeg` and `ffprobe` on `PATH` (only for the video `-m` and audio `-a` modules)

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
```

| Flag | Default | Description |
|---|---|---|
| `-s <dir>` | – | **Required.** Folder containing the song folders (nested category folders are supported) |
| `-o <dir>` | `sanitizer-output/` next to `-s` (symlinks resolved) | Output folder for `invalid/`, `extra-files/`, `originals/` and reports. Must not be inside `-s` |
| `-n` | off | Dry run: log what would be changed and write the reports, but do not modify the library |
| `-t` | **on** | Text-file module (`fixtxt`) |
| `-c` | **on** | Cleanup module |
| `-m` | off | Video module (re-encoding) |
| `-a` | off | Audio module (stereo downmix, normalization) |
| `-v <level>` | `info` | Log level: `debug`, `info`, `warn`, `error` |

`-t` and `-c` are enabled by default. Bool flags are disabled with `=`, e.g. `-t=false`;
`-t false` is rejected because it would stop flag parsing. Modules run in the order
fixtxt → cleanup → video → audio. Exit codes: `0` success, `1` some operations failed (see
the log and the summary line), `2` invalid command line.

> **The tool modifies your library in place.** Run it with `-n` first and keep a backup.
> The text and cleanup modules never delete anything: removed files and broken songs are
> moved into the output folder, mirroring the library's folder structure, so they can be
> restored. The audio module (`-a`) keeps a copy of every file it re-encodes in
> `originals/`. The video module (`-m`) replaces videos in place without a backup (keeping
> them would make the library very large); only originals whose file name changes
> (e.g. `.avi` → `.mp4`) are kept in `extra-files/`.

In dry-run mode every module looks at the library as it is, so a later module does not
see the changes an earlier one would make (e.g. cleanup does not know about a `#MP3` tag
that fixtxt would fix). Folders an earlier module would move away are skipped, though.

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
    to existing files of the right kind, with the exact on-disk spelling
  - moves songs without an UltraStar song `.txt` or without any audio/video to `invalid/`
  - writes `missing_files.txt` (songs without cover / background)
- **cleanup**
  - moves files not referenced by the song's `.txt` to `extra-files/`
  - moves folders with at most one file to `invalid/`
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
<out>/missing_files.txt                      songs without cover / background, ambiguous #MP3
<out>/encoding_issues.txt                    txts left untouched or needing manual repair
<out>/potentiallyCorruptedFiles.txt          videos ffmpeg cannot read (-m)
```

If a target already exists (e.g. from an earlier run), a suffix like ` (2)` is appended;
empty folders are merged into an existing target instead.

## Development

See [CLAUDE.md](CLAUDE.md) for architecture notes, known issues and the roadmap.

```bash
cd src/main/songs-sanitizer
go vet ./... && go test ./...
```
