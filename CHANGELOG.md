# Changelog

All notable user-visible changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- UI: "Delete the backups" only works in a library-manager output folder (one with its marker file); an `originals` folder in any other folder given as output is no longer listed or emptied.
- UI: a set-aside restore or an originals delete requested as a dry run now only plans; before, it moved songs back without recording them for undo, or deleted the backups.
- UI: Stop now works for removing songs: during the check nothing is moved, and during the removal the current move finishes, the remaining songs are listed as skipped and undo puts back what was moved.
- Starting a run no longer deletes every hidden `.*.tmp` file in the output folder: only the tool's own leftover report and journal temp files older than an hour are removed, and each removal is logged.

## [0.2.1] - 2026-11-22

### Added

- MIT license.
- This changelog.

## [0.2.0] - 2026-11-22

### Added

- Browser UI (`ui` subcommand, also started when the program is launched without arguments) with an easy and an advanced mode, a library overview and a "Tidy up my library" task.
- Starting the program by double-click opens its own terminal window; the UI stops when the last browser tab is closed.
- Hidden per-library scan cache for media probes, so repeated runs skip unchanged files (`-no-cache` turns it off).
- `organize`, `dedupe`, `add` and `remove` subcommands with a journal and `-undo`; duplicates are found by content and by similar charts, not only by name.
- ffmpeg/ffprobe detection with a version check.

### Changed

- The program is now called `library-manager` (was `songs-sanitizer`).
- Tidy-up lists folders without a song apart from broken songs.

### Fixed

- Moves to another drive on Windows.
- fixvideos keeps the original when the processed video changed length.
- Processed media is fsynced before it replaces the original.
- fixaudio and fixvideos never process symlinked media.
- organize moves a library song back when its keeper does not arrive, and retries a run whose undo failed.
- A repeated header tag resolves to its last occurrence everywhere.

## [0.1.1] - 2026-11-21

### Fixed

- CI: GitLab jobs run only on runners tagged `docker`.

## [0.1.0] - 2026-11-21

### Added

- Modules fixtxt, cleanup, check (`-k`), fixvideos (`-m`) and fixaudio (`-a`) with dry run (`-n`), one output folder and nothing deleted.
- Release builds for Linux, Windows and macOS.

[Unreleased]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nkreimeyer/ultrastar-library-manager/releases/tag/v0.1.0
