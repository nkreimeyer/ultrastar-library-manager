# Changelog

All notable user-visible changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.2] - 2026-11-22

### Fixed

- UI: "Delete the backups" only works in a library-manager output folder (one with its marker file); an `originals` folder in any other folder given as output is no longer listed or emptied.
- UI: a set-aside restore or an originals delete requested as a dry run now only plans; before, it moved songs back without recording them for undo, or deleted the backups.
- UI: Stop now works for removing songs: during the check nothing is moved, and during the removal the current move finishes, the remaining songs are listed as skipped and undo puts back what was moved.
- Starting a run no longer deletes every hidden `.*.tmp` file in the output folder: only the tool's own leftover report and journal temp files older than an hour are removed, and each removal is logged.
- UI: a check or tidy-up that was stopped (or failed) before it changed anything is no longer shown as carried out; Go ahead can be used again.
- UI: leaving a song alone or joining two songs while a plan is being applied (or just after) no longer changes the plan shown for the run that was already applied; the page is told to wait or check again.
- UI: a request that arrives while the program is shutting down no longer starts a job that would be cut off midway; it is refused with an error.
- A song txt in UTF-32, or in UTF-16 with NUL characters, is reported as an encoding problem and never rewritten; before, it could be rewritten as garbage (and a UTF-32 txt was not recognized as a song txt).
- UI: putting back set-aside items refuses anything that is not a listed song or file (such as a whole category or part of a song), and a song whose old folder outside the library is gone, for example on an unplugged drive, is shown as blocked instead of being put back into a newly created folder.
- A failed `dedupe` or `add` run (or its `-undo`) now says "dedupe failed" or "add failed" instead of "organize failed".
- On macOS and Windows, a library given in another case than its source (`-s ~/songs -l ~/Songs`) is treated as the same folder instead of failing with "library dir must not be inside the source dir"; a source given twice in different case is reported as given twice.
- UI: jobs with very long logs no longer slow down; the log keeps the newest 20000 to 22000 lines instead of exactly 20000.
- UI: the overview no longer counts unreadable folders as folders without a song text; it lists the folders and song texts it could not read separately.
- The media scan cache is no longer thrown away when `ffprobe -version` or `ffmpeg -version` fails or times out on a busy machine; the cache is skipped for that run instead.
- remove: a pattern whose songs all matched an earlier pattern first (e.g. `-song "Elton John - Sacrifice"` after `-artist "Elton John"`) is no longer reported as matching no song; the report's per-pattern count now says it counts the songs a pattern matched first.

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

[Unreleased]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nkreimeyer/ultrastar-library-manager/releases/tag/v0.1.0
