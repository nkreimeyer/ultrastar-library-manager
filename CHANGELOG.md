# Changelog

All notable user-visible changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- In the web UI, an unexpected error in one of the page's background steps (checking the folders, starting a job, handling its end) is shown as a notice ("Something went wrong in this page") instead of being lost silently.
- In the web UI, a run that went through but could not move some songs (or an undo that could not move some back) is no longer titled "Done" or "Undone": it says "Not everything could be done" or "Not everything could be moved back".
- In the web UI, a run stopped before it moved anything says "No song folder was moved" instead of offering an undo that is not there; a stopped removal says "removed", and a single folder is "it".
- In the web UI, "Back to the list" after putting songs back, and "Look at what is set aside" on the overview, list the library just worked on, not the one the main menu's form holds.
- In the web UI, a failed move whose plan cannot be loaded again after a page reload still says "Moving did not finish".

## [0.4.0] - 2026-11-23

### Added

- `export` subcommand and, in the web UI's advanced mode, the task "Export the song list (CSV)": one row per song with artist, title, year, genre, BPM, language, length and duet, comma separated (values with a comma or a double quote are quoted as in RFC 4180), as UTF-8 with a byte order mark for Excel. The library is only read.

## [0.3.1] - 2026-11-23

### Changed

- In the web UI, the buttons that return to the main menu at the end of a job say "Back to the Main Menu"; the ones on the pages in between say "Back" (they were "Start over", "Change folders" and "Change the list").
- In the web UI, "Back" returns to the screen you came from: the tidy-up check goes back to the tidy-up options (the separate "Change what to do" button is gone), the reports go back to the done page when opened from there, and the set-aside list goes back to the overview when opened from there.

## [0.3.0] - 2026-11-22

### Added

- Releases include a macOS binary for Apple Silicon (`library-manager-darwin-arm64`).
- `docs/ffmpeg-windows.md`: how to install ffmpeg on Windows, also without the command line.

### Changed

- `ui` reports leftover arguments, an unknown `-v` level and a bad `-addr` like the other commands (an `Error:` line, the bool-flag hint, the usage).
- The `-undo` help names the UI's restore of set-aside items, which the journal records and undo reverts too.
- GitLab releases keep their binaries in the project's package registry, so the download links no longer expire with the build job's artifacts.
- The debug log no longer calls output folders "sanitizer" output folders.

### Fixed

- UI: the time left no longer reads "1 h 60 min", and sizes no longer show "10.0 KB", "1000 KB" or "1 bytes".
- `-m`: a video whose probe failed because of a read error (a NAS dropping out), a killed ffprobe or unusable ffprobe output is skipped and counted as a failure; before, it was re-encoded, and a non-MP4 one converted with its original deleted, even if it was fine or already processed.
- `-m`: a re-encode during which ffmpeg reported a read error, or whose original could not be read again for the duration check, no longer replaces (or converts and deletes) the original; it counts as a failure and the next run tries again.
- `-m`: a cover picture stored before the video in a file is no longer taken for the video (which could replace the video with a picture-only file, or measure the cover's bitrate and skip a video that needs a re-encode); a result without a video stream never replaces a video.
- `-a`: an encode during which ffmpeg reported a read error, or that came out shorter than the original decodes, no longer replaces the original and marks it as normalized; a peak measured during a read error is no longer used for the gain.
- cleanup no longer moves a song's audio (or video) to `extra-files/` when its `#MP3`/`#AUDIO` (`#VIDEO`) tag names a file of the other kind, e.g. `#MP3:clip.mp4` next to the takes fixtxt asks you to choose from, or the `#VOCALS`/`#INSTRUMENTAL` stem.
- fixtxt corrects an `#MP3`/`#AUDIO` that names the `#VOCALS`/`#INSTRUMENTAL` stem to the song's full mix (or reports it if there are several), instead of keeping it so that the song played only the vocals.
- The media cache now stores results for files whose path, title or other tags contain words like "Timed Out", "Broken Pipe" or "Permission Denied"; before, they were taken for read errors and probed again on every run.
- `-undo`: a song in a folder that cannot be read at the moment (missing permissions, a NAS dropping out) is no longer taken as gone; the undo counts a failure, does not record the run as undone, and a retry moves it back.
- `-undo` no longer puts a song back into a newly created folder when its old source folder outside the library is gone (an unplugged drive, an unmounted share); it reports the folder and can be run again once it is back. Folders of the library itself are still created again, also for an undo given only `-o`.
- organize: after a journal write failed once, nothing more is recorded (before, a later entry could make `-undo` hide a song under a temporary name again).
- Moving a song onto a target that cannot be checked (a read error on a share) is refused instead of possibly replacing what is there.
- UI: without a library, items in `extra-files/` and `invalid/` are shown with an unknown origin instead of being put back into the program's working folder.
- UI: Ctrl-C in the UI's terminal no longer aborts a running ffmpeg encode (which counted as a failure): while the UI runs, the media tools run in their own process group, so the job stops and the running encode finishes.
- A browser the UI started is no longer quit by Ctrl-C in the UI's terminal or when that terminal closes.
- `ui`: a second Ctrl-C (or SIGTERM) while a stop waits for the running job now quits at once instead of being ignored; closing the terminal still lets the job end cleanly.
- A double-click start whose terminal window cannot be opened (osascript without permission to control Terminal, a broken x-terminal-emulator) now starts the UI anyway and logs why, instead of exiting silently.
- `dedupe`'s hint for leftover arguments no longer names `-priority`, a flag `dedupe` does not have.
- A dry-run `-undo` no longer claims to write a plan report.
- `library-manager -h` lists the `ui` subcommand's `-keep-running` and `-v` flags.
- The media cache no longer stores a result for a file whose mtime was less than 2 s old when it was looked up (a same-size rewrite within one FAT timestamp tick during a long probe could be cached).
- The ffmpeg/ffprobe version check no longer hangs past its 5 s timeout when the tool is a wrapper script whose child keeps running.
- `-k -deep` and the duration measurement no longer cache a result whose ffmpeg output names a read error after more than 64 KiB of decode errors.
- organize/dedupe/add: the audio bitrate of a song whose audio file also holds a video of unknown size no longer counts the video's bitrate (cached check results are computed again once).
- `-m` no longer converts a video to another extension (deleting the original) while a second song txt in the folder still names it; it warns and keeps the original.
- A failed txt update during a video conversion is counted as one failure, not two.
- A move to another drive, or a backup copy, that fails no longer deletes a folder or file that another program created at the target in the meantime.
- `-a` and `-m` now process read-only media files on Linux and macOS instead of failing with "Permission denied" (a file replaced in place stays read-only).
- organize/dedupe/add: a source at a drive or share root (`E:\`, `\\server\share`) gets its own named folder below `duplicates/` and `invalid/` instead of none.
- Leftover temp files are only cleaned up in a folder that already is an output folder; other programs' `.<name>.<digits>.tmp` files in a newly chosen `-o` folder are no longer deleted.
- organize, dedupe and add no longer leave a song outside the library when its numbered folder name (" (2)") is held by a library song that stays in place; it gets the next free number.
- A song named after its artist with a dash qualifier ("Iron Maiden - Live", "Black Sabbath - 2009 Remaster") is now grouped with "Iron Maiden (Live)" and "Black Sabbath", so organize, dedupe and add find those duplicates; a title that is a year ("Prince - 1999") is still the song's name.
- UI: putting back a broken song that organize, dedupe or add set aside without a journal record no longer guesses a place below `<library>/<label>/`: the library's own songs go back to their place, and those of another source that the output folder names (its `duplicates/` folder or the journal) are shown as of unknown origin.
- The reason of a joined duplicate names how it was joined (same notes, similar chart and lyrics, or your "Same song" choice), and no longer says "by its notes" for songs joined in the review.
- A category folder holding only symlinked song folders is no longer treated as a broken song and moved to `invalid/` (symlinked folders are logged and not followed).
- fixtxt replaces an `#MP3`/`#AUDIO` that names an image or another non-media file with the video of a video-only song.
- cleanup (with `-t=false`) sets aside a folder that only its leftover files kept from looking empty in the same run, not on the next one.
- A song txt read as CP1252 or another single-byte encoding that contains UTF-8 replacement characters (U+FFFD) is reported in `encoding_issues.txt` and no longer rewritten with the damage turned into "ï¿½".
- cleanup with `-t=false` no longer moves files that only another song txt in the same folder references.
- fixtxt no longer points `#COVER` and `#BACKGROUND` at the same image when a tag value is written in another Unicode normalization (NFD, macOS).
- A repeated `#ENCODING` tag resolves to its last occurrence, as in UltraStar, instead of the first.
- A txt that is valid UTF-8 only by accident is read as CP1250 when `#LANGUAGE` names a Central European language, and is reported when its CP1252 reading looks like CP1250.
- `-m` can update song txts with CR-only (or mixed) line endings when it converts a video to `.mp4`.
- UI: while songs are removed, the progress of reading the songs is shown again; before, it stayed at the full count until the moves started.
- UI: the check review no longer counts broken songs whose song txt has an encoding problem as songs to set aside; it says they stay, as the apply does.
- UI: the overview counts a song folder with a second txt file it cannot read as unreadable, not as a folder without a song txt.
- UI: the page works when the UI listens on port 80 (browsers leave the port out of the address).
- UI: a check no longer shows "Moving songs" while it plans the moves.
- UI: the list of earlier runs always belongs to the library shown, and "Undo this run" undoes a run of that list; before, a slow answer for a library picked earlier could replace it.
- UI: when Stop does not go through, the page says so and Stop can be pressed again.
- UI: an organize or remove apply that was stopped or failed before it moved anything can be applied again without checking again.
- UI: a run that failed part-way no longer says you stopped it.
- UI: the counts of broken songs on the check review and of the songs a removal takes out are colored like the other counts.
- UI: report sizes use the same units as the rest of the page, and paths shown on Windows use backslashes throughout.

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

- Browser UI (`ui` subcommand, also started when the program is launched without arguments) with an easy and an advanced mode, a library overview and a "Tidy up my library" task, which lists folders without a song apart from broken songs.
- Starting the program by double-click opens its own terminal window; the UI stops when the last browser tab is closed.
- Hidden per-library scan cache for media probes, so repeated runs skip unchanged files (`-no-cache` turns it off).
- `organize`, `dedupe`, `add` and `remove` subcommands with a journal and `-undo`; duplicates are found by content and by similar charts, not only by name.
- ffmpeg/ffprobe detection with a version check.

### Changed

- The program is now called `library-manager` (was `songs-sanitizer`).

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

[Unreleased]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/nkreimeyer/ultrastar-library-manager/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nkreimeyer/ultrastar-library-manager/releases/tag/v0.1.0
