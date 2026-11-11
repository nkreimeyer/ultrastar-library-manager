package organize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBucket(t *testing.T) {
	tests := []struct {
		artist    string
		ignoreThe bool
		want      string
	}{
		{"ABBA", false, "A"},
		{"abba", false, "A"},
		{"  Queen", false, "Q"},
		// Diacritics and letters without a decomposition.
		{"Ärzte", false, "A"},
		{"Österreich", false, "O"},
		{"über", false, "U"},
		{"Édith Piaf", false, "E"},
		{"Ørjan", false, "O"},
		{"øystein", false, "O"},
		{"Æther", false, "A"},
		{"Œuvre", false, "O"},
		{"Łzy", false, "L"},
		{"Đorđe Balašević", false, "D"},
		{"Ðoe", false, "D"},
		{"Þrymr", false, "T"},
		{"ßtyle", false, "S"},
		{"ẞTYLE", false, "S"},
		{"ıstanbul", false, "I"},
		{"İstanbul", false, "I"},
		{"Ｆｕｌｌｗｉｄｔｈ", false, "F"},
		{"ﬁve", false, "F"},
		{"Ñandú", false, "N"},
		{"Çelik", false, "C"},
		{"Ħamrun", false, "H"},
		// Digits, also of other scripts.
		{"2 Unlimited", false, "0-9"},
		{"50 Cent", false, "0-9"},
		{"٣ Arabic-Indic", false, "0-9"},
		{"३ Devanagari", false, "0-9"},
		{"７ Fullwidth", false, "0-9"},
		// Leading characters that are neither letters nor digits.
		{"'Til Tuesday", false, "T"},
		{".38 Special", false, "0-9"},
		{"(hed) p.e.", false, "H"},
		{"!!!", false, "#"},
		{"¡Forward, Russia!", false, "F"},
		{"…And You Will Know Us", false, "A"},
		{"$uicideboy$", false, "U"},
		{"² Superscript", false, "S"}, // '²' is a number but not a decimal digit
		{"♥ Love", false, "L"},
		// Other scripts and nothing usable.
		{"Кино", false, "#"},
		{"Ария", false, "#"},
		{"Ελληνικά", false, "#"},
		{"宇多田ヒカル", false, "#"},
		{"BTS 방탄소년단", false, "B"},
		{"방탄소년단", false, "#"},
		{"", false, "#"},
		{"   ", false, "#"},
		{"♥♥", false, "#"},
		{"\xff\xfe", false, "#"},
		// "The" handling.
		{"The Beatles", false, "T"},
		{"The Beatles", true, "B"},
		{"the beatles", true, "B"},
		{"THE WHO", true, "W"},
		{"The\tWho", true, "W"},
		{"The  Who", true, "W"},
		{"The 1975", true, "0-9"},
		{"The The", true, "T"},
		{"The", true, "T"},
		{"The ", true, "T"},
		{"The !!!", true, "T"},
		{"Theory of a Deadman", true, "T"},
		{"Them", true, "T"},
		{"The-Dream", true, "T"},
		{"'The Band'", true, "B"},
		{"The Ärzte", true, "A"},
		{"Die Ärzte", true, "D"},
	}
	for _, tt := range tests {
		if got := Bucket(tt.artist, tt.ignoreThe); got != tt.want {
			t.Errorf("Bucket(%q, %v) = %q, want %q", tt.artist, tt.ignoreThe, got, tt.want)
		}
	}
}

func TestFolderName(t *testing.T) {
	tests := []struct {
		artist, title, want string
	}{
		{"ABBA", "Dancing Queen", "ABBA - Dancing Queen"},
		{"  ABBA ", "\tDancing   Queen\n", "ABBA - Dancing Queen"},
		// NFC.
		{"Bjo\u0308rk", "Jo\u0301ga", "Björk - Jóga"},
		// Separators.
		{"AC/DC", "Back in Black", "AC-DC - Back in Black"},
		{`AC\DC`, "T.N.T.", "AC-DC - T.N.T"},
		{"A|B", "C", "A-B - C"},
		// ':' never adds a " - " separator.
		{"Re:Zero", "Theme", "Re-Zero - Theme"},
		{"Artist", "Title: Sub", "Artist - Title Sub"},
		{"Artist", "Title : Sub", "Artist - Title Sub"},
		{"Artist", "Title :Sub", "Artist - Title Sub"},
		{"Artist", ":Start", "Artist - Start"},
		{"Artist", "End:", "Artist - End"},
		{"Artist", "10:30", "Artist - 10-30"},
		// Dropped and replaced characters.
		{"Artist", "What's Up?", "Artist - What's Up"},
		{"Artist", "Who * Are <You>", "Artist - Who Are You"},
		{"Artist", `Say "Hello"`, "Artist - Say 'Hello'"},
		{"Art\x00ist", "Ti\x1ftle", "Artist - Title"},
		{"\uFEFFArtist", "Ti\u200Btle", "Artist - Title"},
		{"Artist", "Ti\u00ADtle", "Artist - Title"}, // soft hyphen is a format character
		{"Artist", "Line\nBreak", "Artist - Line Break"},
		{"Artist", "No\u00A0Break", "Artist - No Break"},
		{"Art\xffist", "Title", "Artist - Title"}, // invalid UTF-8 dropped
		// Dots and spaces at the ends.
		{"R.E.M.", "Losing My Religion", "R.E.M. - Losing My Religion"},
		{"Artist", "Mr.", "Artist - Mr"},
		{"Artist", "Title. . .", "Artist - Title"},
		{".38 Special", "Hold On Loosely", "38 Special - Hold On Loosely"},
		{"...And You Will Know Us", "Title", "And You Will Know Us - Title"},
		{"Britney Spears", "...Baby One More Time", "Britney Spears - ...Baby One More Time"},
		// Qualifiers are kept.
		{"Artist", "Song [DUET]", "Artist - Song [DUET]"},
		{"Artist", "Song (Live)", "Artist - Song (Live)"},
		// Placeholders, never empty, "." or "..".
		{"", "", "Unknown Artist - Unknown Title"},
		{".", "..", "Unknown Artist - Unknown Title"},
		{"???", "***", "Unknown Artist - Unknown Title"},
		{"Artist", "", "Artist - Unknown Title"},
		{"", "Title", "Unknown Artist - Title"},
		// Reserved device names: only possible with a dot in the artist.
		{"CON", "Title", "CON - Title"},
		{"CON.x", "Title", "CON_.x - Title"},
		{"nul.band", "Title", "nul_.band - Title"},
		{"Com1.Crew", "Title", "Com1_.Crew - Title"},
		{"LPT9.", "Title", "LPT9_. - Title"},
		{"CONSOLE.x", "Title", "CONSOLE.x - Title"},
		{"COM0.x", "Title", "COM0.x - Title"},
	}
	for _, tt := range tests {
		got := FolderName(tt.artist, tt.title)
		if got != tt.want {
			t.Errorf("FolderName(%q, %q) = %q, want %q", tt.artist, tt.title, got, tt.want)
		}
		checkSafeName(t, got)
	}
}

// checkSafeName fails if name breaks one of the FolderName guarantees.
func checkSafeName(t *testing.T, name string) {
	t.Helper()
	if name == "" || name == "." || name == ".." {
		t.Errorf("unsafe name %q", name)
	}
	if !utf8.ValidString(name) {
		t.Errorf("name %q is not valid UTF-8", name)
	}
	if len(name) > MaxFolderNameBytes {
		t.Errorf("name %q has %d bytes, more than %d", name, len(name), MaxFolderNameBytes)
	}
	if strings.ContainsAny(name, `/\|:?*<>"`) {
		t.Errorf("name %q contains a character invalid on Windows/macOS", name)
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, " ") {
		t.Errorf("name %q starts with a dot or space", name)
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		t.Errorf("name %q ends with a dot or space", name)
	}
	if strings.Count(name, " - ") < 1 {
		t.Errorf("name %q has no artist/title separator", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			t.Errorf("name %q contains a control character", name)
		}
	}
}

func TestFolderNameLength(t *testing.T) {
	long := func(s string, n int) string { return strings.Repeat(s, n) }
	tests := []struct {
		name          string
		artist, title string
		wantArtist    string // expected artist part ("" = unchanged)
		titleMin      int    // the title keeps at least this many bytes
	}{
		{"short", "Artist", "Title", "", 5},
		{"long title", "Artist", long("x", 300), "", 180 - 3 - 6},
		{"long multibyte title", "Artist", long("ä", 200), "", 170},
		{"long emoji title", "Artist", long("😀", 100), "", 168},
		{"long artist", long("a", 300), "Title", long("a", 180-3-5), 5},
		{"both long", long("a", 300), long("t", 300), long("a", 180-3-40), 40},
		{"both long multibyte", long("ö", 300), long("ü", 300), "", 38},
		{"title ends in spaces after cut", "Artist", long("x", 170) + "     . . . tail" + long("y", 20), "", 160},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FolderName(tt.artist, tt.title)
			checkSafeName(t, got)
			a, title, ok := strings.Cut(got, " - ")
			if !ok {
				t.Fatalf("no separator in %q", got)
			}
			if tt.wantArtist != "" && a != tt.wantArtist {
				t.Errorf("artist part = %q (%d bytes), want %d bytes", a, len(a), len(tt.wantArtist))
			}
			if len(title) < tt.titleMin {
				t.Errorf("title part has %d bytes, want at least %d", len(title), tt.titleMin)
			}
			if len(tt.artist)+3+len(tt.title) > MaxFolderNameBytes && len(got) < MaxFolderNameBytes-4 {
				t.Errorf("cut too much: %d bytes", len(got))
			}
		})
	}
}

func TestFolderNameTitleOfDots(t *testing.T) {
	// The cut leaves only dots, which are trimmed: the title becomes a placeholder.
	if got := FolderName("Artist", strings.Repeat(".", 200)+"x"); got != "Artist - _" {
		t.Errorf("got %q, want %q", got, "Artist - _")
	}
}

func TestFolderNameShortensTitleFirst(t *testing.T) {
	artist := strings.Repeat("a", 100)
	got := FolderName(artist, strings.Repeat("t", 200))
	if !strings.HasPrefix(got, artist+" - ") {
		t.Errorf("artist was shortened although the title could be: %q", got)
	}
	if len(got) != MaxFolderNameBytes {
		t.Errorf("len = %d, want %d", len(got), MaxFolderNameBytes)
	}
}

func TestNameKey(t *testing.T) {
	same := [][2]string{
		{"ABBA - Dancing Queen", "abba - dancing queen"},
		{"Bjo\u0308rk - Jo\u0301ga", "Bj\u00f6rk - J\u00f3ga"},
		{"BJÖRK", "björk"},
		{"Straße", "STRASSE"},
		{"A/ABBA - X", "a/abba - x"},
	}
	for _, p := range same {
		if NameKey(p[0]) != NameKey(p[1]) {
			t.Errorf("NameKey(%q) != NameKey(%q)", p[0], p[1])
		}
	}
	different := [][2]string{
		{"ABBA - Dancing Queen", "ABBA - Dancing Queen (2)"},
		{"Bjork", "Björk"},
		{"A - B", "A- B"},
	}
	for _, p := range different {
		if NameKey(p[0]) == NameKey(p[1]) {
			t.Errorf("NameKey(%q) == NameKey(%q)", p[0], p[1])
		}
	}
}
