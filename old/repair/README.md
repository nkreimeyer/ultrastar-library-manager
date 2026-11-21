# repair (archived)

The `repair` subcommand put back characters that an earlier lossy decoding had turned into
U+FFFD (`�`). It was needed once, for the songs.organized library (2026-11-20: 1,203
damaged txts, 19,183 U+FFFD, 17,948 repaired with `-lyrics`), and was archived on
2026-11-21 because its tests took longer than all the rest of the suite.

This folder is outside the Go module (`src/main/library-manager`), so it is neither built
nor tested. What is here:

- `repairtxt/`: the package as it was.
- `repair_cmd.go`: the CLI subcommand (package main).
- `webui_repair.go` and `webui_options_test.go`: the UI handlers and their option tests
  (webui/repair.go and webui/options_test.go).
- `webui_flow_test.go.txt` and `cmd_test.go.txt`: the repair tests that were cut out of
  webui/flow_test.go and the main package's tests.
- `webui_app_repair.js.txt` and `webui_style_repair.css.txt`: the repair parts of the UI's
  app.js and style.css.

To bring it back, move the files to their old places, put the `repair` dispatch and usage
line back into main.go, put the `repair` field and the `/api/repair/*` routes back into
webui/server.go, put the JS and CSS fragments back, and restore `utils.KnownLanguageCode`
(git history of the commit that archived it). `utils.LanguageCodes` stayed in the module,
because `utils.DecodeTxt` uses it.

## Architecture notes (from CLAUDE.md)

- repair (`repairtxt`): every U+FFFD is one lost CP1252/CP1250 byte, so it is replaced by
  exactly one rune that one of those codepages has (`allowedRune`); only the U+FFFD bytes
  of the raw file change (tested: originals and repaired files differ nowhere else). Only
  UTF-8 txts without a decode Problem are repaired. A fix with a rune outside `allowedRune`
  (e.g. an upper-cased `µ`/`ƒ`) is dropped. The totals (`Report.Fixed`/`Weak`) count only
  written txts (in a dry run: those that would be written); an excluded, stopped or failed
  txt keeps its plan in its `FileReport`, its `Left` is what it still holds, and the text
  report labels its fixes `planned`. `-wordlist lang=` must name a code `languagePrefixes`
  has, or one language by name (CLI exit 2, UI 400). In `languagePrefixes` the first match
  wins, so names that start like another language get their own code or an empty one
  (`Esperanto`, `Frisian`, `Latino`, `Niederdeutsch`, `Romani`). Words are rebuilt across note syllables
  (`~` is transparent, breaks and player markers end a line). Evidence levels in order:
  reviewed spellings (`-words`, unique), song (the folder name and subfolder names uncut,
  file names without the extension, fetched lyrics, own undamaged words; min 1, ratio 3;
  a file tag such as `#MP3` is filled only from a whole file name of the same length), corpus of
  the song's languages (min 2, ratio 5), word lists (unique), global corpus (weak if the song
  has a language); an undecided level narrows the candidate pool for the next. Then `¿`/`¡`
  by the line's `?`/`!` (Spanish only), bigram context, apostrophe split, the char n-gram
  model (weak = suggestion only), and edge punctuation only if no letter forms a known
  4-gram there (never an apostrophe: words with edge apostrophes/punctuation are corpus
  forms of their own, `goin’`, `¡oh`). Words still left that are split by a hyphen
  syllable (`l� - cher - lich`) are looked up joined, known words only (never the
  spelling model: `j�ng - ste` would become `jängste` after `Angst`). Languages =
  `#LANGUAGE` codes plus the detected one if it beats them by 0.5 nats/word; detection is
  naive Bayes with Jelinek-Mercer smoothing against the whole corpus (add-one or Dirichlet
  smoothing let a word missing from both corpora cost a big corpus more, so songs drifted
  to the small Dutch corpus). lrclib lookups fall back to first artist + title without
  `(…)`/`[…]`, then the title alone; titles and artists match with `utils.FoldString`, rune by rune (a U+FFFD
  stands for one non-ASCII rune) and, without U+FFFD, also as whole folded keys (`Mø` = `Mo`,
  `Straße` = `Strasse`; `й` ≠ `и`); old
  "not found" cache entries (no `version` 2) are looked up again. Lookups take the run's ctx
  (a cancel stops a request or a rate-limit wait; Run returns the ctx error right after the
  lookup); an answer whose cache entry cannot be written is still used; a `-lyrics-cache`
  inside `-s` is refused (CLI exit 2, UI 400: it would become a song folder). The mid-line case of a
  repaired first letter follows the song's own lexicon (lyrics) before the corpus. The song's
  own lexicon is built from its oldest copy in `originals/` if there is one (`oldestOriginal`),
  so words an earlier run repaired are no evidence (a context-repaired `får` decided the
  song's other `F�r` before the lyrics were asked). In `byContext` a candidate that alone
  has the song's own bigrams wins over the corpus; otherwise they count twice next to it. It
  comes first in `spell`
  (`localSpellAt`: two distinct 5-gram windows support one letter, none another; counts and
  4-grams of one song are noise, `très` made `dor�s` = `dorès`). An ASCII `'` in fetched
  lyrics is read as the song's apostrophe (`songApostrophe`, default `’`).
  Txts with a copy in `<out>/originals/` (repaired
  before) are kept out of the corpus so repairs never become evidence. On songs.organized
  (14,871 txts, 2026-11-20, txt-only copy): 1,203 damaged txts, 19,183 U+FFFD; 15,697 repaired
  offline, 17,948 with `-lyrics` and the reviewed `Täterätätä`/`Määänner` (589 weak); no
  file had NUL bytes.

## The README section of the time

## Repairing damaged txts (`repair`)

```bash
library-manager repair -s <song-dir> [-wordlist [lang=]<file>]... [-words <file>]... [-lyrics] [-strict] [-o <out>] [-n] [-v level]
```

An earlier conversion (an old version of this tool, or another program) left some txts with
`�` (U+FFFD, the replacement character) in place of every character it could not
decode: `f�r`, `I�m`, `Gl�ck`. fixtxt and check list these files (`encoding_issues.txt`,
`damaged-text`); `repair` puts the characters back. Each `�` stands for exactly one lost
CP1252/CP1250 character, so it is replaced by exactly one character. Nothing else in the
file changes (line endings, BOM and all other bytes stay), and the original of every
rewritten txt is copied to `<out>/originals/` first (`-o` defaults to `sanitizer-output/`
next to `-s`).

What a damaged word stood for is decided from evidence, strongest first:

0. **your reviewed spellings** from `-words <file>` (one or more words per line, e.g.
   `Täterätätä`): a damaged word that exactly one of them fits is repaired that way in every
   song. Words below a line `[Artist - Title]` apply to that song only and come first there,
   for words that differ between songs (`f�hrt` is `fährt` in one song, `führt` in another);
   `[*]` goes back to words for every song:

   ```
   Täterätätä
   [Patrick Lindner - Mandeln und Zimt]
   fährt
   [Eisregen - Farbenfinsternis, Kapitel 4 - Mein Reich komme]
   Hülle Mühen
   [*]
   Säcken
   ```

   The name in brackets is compared with the song's folder name, its txt name and its
   `#ARTIST - #TITLE`; case, accents, punctuation and a U+FFFD in the name do not matter.
   Sections that match no damaged txt are listed in the report;
1. **the song itself**: its folder name, the names of the files and folders in it
   (`Drömhus - Vill Ha Dig.mp3`; a dot in a folder name is no extension, `Dr. Alban - …`),
   its lyrics from lrclib.net with `-lyrics`, and its own undamaged words;
2. **the library**: all undamaged song txts in the song's language (`#LANGUAGE`, plus the
   language detected from its words when the tag is missing or clearly wrong);
3. **word lists** given with `-wordlist`, e.g. a German hunspell dictionary
   (`-wordlist de=/usr/share/hunspell/de_DE.dic`; plain one-word-per-line lists and
   frequency lists work too, and a list without `lang=` applies to every song; `lang=` is
   a language code or name, and one no song can have, such as `ge=`, is refused);
4. the undamaged txts of all languages.

A level decides only if one candidate clearly wins (`für` over `fór`); otherwise the next
level breaks the tie. Ambiguous words (Swedish `f�r` = `för` or `får`) are decided by
the neighbouring words if the song itself (its lyrics, its undamaged lines) or the library
has seen those pairs (a pair only the song has wins: its lyrics' `För jag tror` beats the
library's many `får jag`), else left as they are. The
library also knows words with their edge apostrophes and punctuation (`goin’`, `¡Oh`), so
`goin�` becomes `goin’` but `tomb�` becomes `tombé`. A word that no list has as a whole is
repaired by its parts at an apostrophe (`d'�tre` = `d'être`), as one word with its
hyphen-split syllables (`l� - cher - lich` = `lächerlich`), or by letter combinations:
first the song's own (`aufzuhören` in the lyrics spells `aufzuh�r'n`; only if two different
five-letter combinations around the gap agree and none points to another letter), then those
of its language (`revoluci�n` = `revolución`). An apostrophe written `'` in the fetched
lyrics counts as the song's own apostrophe (`’`, or `´` if the song uses that), because a lost
character was never plain ASCII: `j'espère` repairs `j�esp�re`. Case follows the word
(`F�R` = `FÜR`, `�rger` = `Ärger` mid-line). Tags naming files (`#MP3`, `#VIDEO`, …) are
only repaired by matching a file in the folder.

Run with `-n` first and read `<out>/fffd_repair.txt`: it lists every damaged txt (U+FFFD
before → left), every repair with its evidence and lines, and every word left with its lyric
line and candidates. A txt that was not written (excluded in the UI, a stopped run, a failed
write) lists its repairs as `planned` and does not count in the totals. Put the answers for words left over into a `-words` file and run
again. **Weak** repairs (a single candidate seen once, or a word the song's
language never uses) are applied but marked for review; `-strict` leaves them undone.
Spelling-model guesses with little evidence are never applied, only suggested. The same
data is in `fffd_repair.json`.

With `-lyrics`, artist and title of every song that still has unrepaired words after the
offline pass are sent to [lrclib.net](https://lrclib.net) (no account needed). The answers
are cached in `<out>/lyrics-cache/` (`-lyrics-cache` to choose another folder, never one
inside the song dir), so a second run sends no request for the same song; if the cache
cannot be written, the answer is still used for this run. A song that is not found by artist and title is
searched again by its first artist (`Shakira feat. …` = `Shakira`) and its title without
bracketed qualifiers (`(Duett)`, `(Live)`, `[VIDEO]`), then by that title alone; titles match
without accents (`Desole'` = `Désolé`). A word's capital letter follows the song's lyrics
(`of Ísland`) before the library's habit. Txts repaired by an earlier run (they have a copy
in `originals/`) are left out of the library evidence, and a song's own evidence comes from
its oldest copy there, so a second run decides like the first and never builds on its own
earlier repairs. Txts with a decoding problem, txts that are not UTF-8 (run fixtxt first) and
symlinked txts are listed as skipped. A repaired `#ARTIST`/`#TITLE` does not rename the
folder; `organize` with the library as a source does.

