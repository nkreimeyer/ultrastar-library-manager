package organize

import "testing"

type songID struct {
	artist, title string
	duet          bool
}

func TestSongKeyEquivalent(t *testing.T) {
	tests := []struct {
		name string
		a, b songID
	}{
		{"case", songID{"ABBA", "Dancing Queen", false}, songID{"abba", "DANCING QUEEN", false}},
		{"whitespace", songID{" ABBA ", "Dancing  Queen", false}, songID{"ABBA", "Dancing Queen", false}},
		{"NFC vs NFD", songID{"Bjo\u0308rk", "Jo\u0301ga", false}, songID{"Bj\u00f6rk", "J\u00f3ga", false}},
		{"diacritics", songID{"Beyoncé", "Halo", false}, songID{"Beyonce", "Halo", false}},
		{"umlaut", songID{"Die Ärzte", "Männer sind Schweine", false}, songID{"Die Arzte", "Manner sind Schweine", false}},
		{"sharp s", songID{"Rammstein", "Weißes Fleisch", false}, songID{"Rammstein", "Weisses Fleisch", false}},
		{"ø", songID{"Mø", "Lean On", false}, songID{"Mo", "Lean On", false}},
		{"typographic apostrophe", songID{"Guns N’ Roses", "Sweet Child O’ Mine", false}, songID{"Guns N' Roses", "Sweet Child O' Mine", false}},
		{"apostrophe removed", songID{"Artist", "Don't Stop", false}, songID{"Artist", "Dont Stop", false}},
		{"acute as apostrophe", songID{"Artist", "Don´t Stop", false}, songID{"Artist", "Don't Stop", false}},
		{"ampersand", songID{"Simon & Garfunkel", "The Boxer", false}, songID{"Simon and Garfunkel", "The Boxer", false}},
		{"plus", songID{"Simon + Garfunkel", "The Boxer", false}, songID{"Simon And Garfunkel", "The Boxer", false}},
		{"ampersand without spaces", songID{"Simon&Garfunkel", "The Boxer", false}, songID{"Simon & Garfunkel", "The Boxer", false}},
		{"leading the", songID{"The Beatles", "Help!", false}, songID{"Beatles", "Help", false}},
		{"slash vs dash", songID{"AC/DC", "Back in Black", false}, songID{"AC-DC", "Back in Black", false}},
		{"dots", songID{"R.E.M.", "Losing My Religion", false}, songID{"REM", "Losing My Religion", false}},
		{"punctuation", songID{"Artist", "Hello, World!", false}, songID{"Artist", "Hello World", false}},
		{"feat in artist", songID{"Rihanna feat. Jay-Z", "Umbrella", false}, songID{"Rihanna", "Umbrella", false}},
		{"ft in artist", songID{"Rihanna ft. Jay-Z", "Umbrella", false}, songID{"Rihanna", "Umbrella", false}},
		{"ft without dot", songID{"Rihanna ft Jay-Z", "Umbrella", false}, songID{"Rihanna", "Umbrella", false}},
		{"featuring", songID{"Rihanna Featuring Jay-Z", "Umbrella", false}, songID{"Rihanna", "Umbrella", false}},
		{"feat in artist brackets", songID{"Rihanna (feat. Jay-Z)", "Umbrella", false}, songID{"Rihanna", "Umbrella", false}},
		{"feat in title brackets", songID{"Rihanna", "Umbrella (feat. Jay-Z)", false}, songID{"Rihanna", "Umbrella", false}},
		{"feat in title brackets []", songID{"Rihanna", "Umbrella [ft. Jay-Z]", false}, songID{"Rihanna", "Umbrella", false}},
		{"feat in title", songID{"Rihanna", "Umbrella feat. Jay-Z", false}, songID{"Rihanna", "Umbrella", false}},
		{"feat before qualifier", songID{"Artist", "Song feat. X (Live)", false}, songID{"Artist", "Song (Live)", false}},
		{"remastered", songID{"Queen", "Bohemian Rhapsody (Remastered 2011)", false}, songID{"Queen", "Bohemian Rhapsody", false}},
		{"remaster year first", songID{"Queen", "Bohemian Rhapsody [2011 Remaster]", false}, songID{"Queen", "Bohemian Rhapsody", false}},
		{"remastered version", songID{"Queen", "Bohemian Rhapsody (Remastered Version)", false}, songID{"Queen", "Bohemian Rhapsody", false}},
		{"digitally remastered", songID{"Queen", "Bohemian Rhapsody (Digitally Remastered)", false}, songID{"Queen", "Bohemian Rhapsody", false}},
		{"dash remaster", songID{"Queen", "Bohemian Rhapsody - Remastered 2011", false}, songID{"Queen", "Bohemian Rhapsody", false}},
		{"official video", songID{"Artist", "Song (Official Video)", false}, songID{"Artist", "Song", false}},
		{"official music video", songID{"Artist", "Song [Official Music Video]", false}, songID{"Artist", "Song", false}},
		{"video", songID{"Artist", "Song (Video)", false}, songID{"Artist", "Song", false}},
		{"hd", songID{"Artist", "Song [HD]", false}, songID{"Artist", "Song", false}},
		{"hq", songID{"Artist", "Song (HQ)", false}, songID{"Artist", "Song", false}},
		{"4k", songID{"Artist", "Song (4K)", false}, songID{"Artist", "Song", false}},
		{"karaoke", songID{"Artist", "Song (Karaoke)", false}, songID{"Artist", "Song", false}},
		{"karaoke version", songID{"Artist", "Song (Karaoke Version)", false}, songID{"Artist", "Song", false}},
		{"ultrastar", songID{"Artist", "Song [UltraStar]", false}, songID{"Artist", "Song", false}},
		{"singstar", songID{"Artist", "Song (SingStar)", false}, songID{"Artist", "Song", false}},
		{"explicit", songID{"Artist", "Song (Explicit)", false}, songID{"Artist", "Song", false}},
		{"album version", songID{"Artist", "Song (Album Version)", false}, songID{"Artist", "Song", false}},
		{"single version", songID{"Artist", "Song (Single Version)", false}, songID{"Artist", "Song", false}},
		{"plain year", songID{"Artist", "Song (1984)", false}, songID{"Artist", "Song", false}},
		{"several noise groups", songID{"Artist", "Song (Remastered) [HD] {1999}", false}, songID{"Artist", "Song", false}},
		{"live in different brackets", songID{"Artist", "Song (Live)", false}, songID{"Artist", "Song [Live]", false}},
		{"live as dash suffix", songID{"Artist", "Song - Live", false}, songID{"Artist", "Song (Live)", false}},
		{"live and remaster", songID{"Artist", "Song (Live) [Remastered]", false}, songID{"Artist", "Song (Live)", false}},
		{"duet flag vs qualifier", songID{"Artist", "Song [DUET]", false}, songID{"Artist", "Song", true}},
		{"duet qualifier spellings", songID{"Artist", "Song (Duet)", false}, songID{"Artist", "Song [DUET]", false}},
		{"duet version", songID{"Artist", "Song (Duet Version)", false}, songID{"Artist", "Song", true}},
		{"duett german", songID{"Artist", "Song (Duett)", false}, songID{"Artist", "Song", true}},
		{"duet dash", songID{"Artist", "Song - Duet", false}, songID{"Artist", "Song", true}},
		{"duet qualifier and flag", songID{"Artist", "Song [DUET]", true}, songID{"Artist", "Song", true}},
		{"fullwidth", songID{"ＡＢＢＡ", "Ｓｏｓ", false}, songID{"ABBA", "SOS", false}},
		{"cyrillic case", songID{"Кино", "Группа крови", false}, songID{"КИНО", "ГРУППА КРОВИ", false}},
		{"unbalanced bracket", songID{"Artist", "Song (Live", false}, songID{"Artist", "Song Live", false}},
		{"leading bracket is part of the name", songID{"The Rolling Stones", "(I Can't Get No) Satisfaction", false}, songID{"Rolling Stones", "I Cant Get No Satisfaction", false}},
		{"invalid utf8 dropped", songID{"Art\xffist", "Song", false}, songID{"Artist", "Song", false}},
		{"soft hyphen", songID{"Oasis", "Wonder\u00adwall", false}, songID{"Oasis", "Wonderwall", false}},
		{"zero-width space", songID{"Oasis", "Wonder\u200bwall", false}, songID{"Oasis", "Wonderwall", false}},
		{"zero-width joiner", songID{"Oasis", "Wonder\u200dwall", false}, songID{"Oasis", "Wonderwall", false}},
		{"zero-width no-break space", songID{"Oa\ufeffsis", "Wonder\ufeffwall", false}, songID{"Oasis", "Wonderwall", false}},
		{"tab is a space", songID{"Oasis", "Wonder\twall", false}, songID{"Oasis", "Wonder wall", false}},
		{"cyrillic NFC vs NFD", songID{"Кино", "Мой", false}, songID{"Кино", "Мои\u0306", false}},
		{"kana NFC vs NFD", songID{"Kenshi", "カゲ", false}, songID{"Kenshi", "カケ\u3099", false}},
		{"halfwidth kana", songID{"Kenshi", "ｶﾞ", false}, songID{"Kenshi", "ガ", false}},
		{"greek accents", songID{"Βίκυ", "Σ' αγαπώ", false}, songID{"Βικυ", "Σ' αγαπω", false}},
		{"feat then dash qualifier", songID{"Artist", "Song feat. X - Live", false}, songID{"Artist", "Song (Live)", false}},
		{"feat dash suffix", songID{"Artist", "Song - feat. X", false}, songID{"Artist", "Song", false}},
		{"duet group with other qualifier", songID{"Artist", "Song [Duet - Acoustic]", false}, songID{"Artist", "Song (Acoustic) [DUET]", false}},
		{"duet with names and qualifier", songID{"Artist", "Song (Duet with B, Live)", false}, songID{"Artist", "Song (Duet Version) [Live]", false}},
		{"duet with names", songID{"Artist", "Song (Duett mit Peter Maffay)", false}, songID{"Artist", "Song [DUET]", false}},
		{"duet noise", songID{"Artist", "Song (Duet Remastered)", false}, songID{"Artist", "Song [DUET]", false}},
		{"duet in a middle dash part", songID{"Artist", "Song - Duet - Live", false}, songID{"Artist", "Song (Live)", true}},
		// Spaces and hyphens, the (The) suffix, collaborations, the artist in the title.
		{"spaces ignored", songID{"Artist", "Rock n Roll", false}, songID{"Artist", "Rocknroll", false}},
		{"hyphen in the artist", songID{"A-ha", "Take on Me", false}, songID{"Aha", "Take on me", false}},
		{"slash in the artist", songID{"AC/DC", "T.N.T.", false}, songID{"ACDC", "TNT", false}},
		{"split band name", songID{"BIGBANG", "Haru Haru", false}, songID{"Big Bang", "Haru Haru", false}},
		{"joined title", songID{"ABBA", "Ring Ring", false}, songID{"ABBA", "RingRing", false}},
		{"(The) suffix", songID{"Cranberries (The)", "Linger", false}, songID{"The Cranberries", "Linger", false}},
		{", The suffix", songID{"Beatles, The", "Help!", false}, songID{"Beatles", "Help!", false}},
		{"collaboration order", songID{"Kate Bush & Peter Gabriel", "Don't Give Up", false}, songID{"Peter Gabriel and Kate Bush", "Dont Give Up", false}},
		{"collaboration x", songID{"Udo Lindenberg x Apache 207", "Komet", false}, songID{"Udo Lindenberg & Apache 207", "Komet", false}},
		{"collaboration with", songID{"Aimer with Chelly", "Ninelie", false}, songID{"Chelly + Aimer", "Ninelie", false}},
		{"collaboration vs", songID{"Linkin Park vs. Jay-Z", "Numb", false}, songID{"Jay-Z & Linkin Park", "Numb", false}},
		{"three names", songID{"A & B & C", "Song", false}, songID{"C x A with B", "Song", false}},
		{"artist in the title", songID{"James Blunt", "James Blunt-Wise Men", false}, songID{"James Blunt", "Wise Men", false}},
		{"artist in the title with a dash", songID{"Die Ärzte", "Die Ärzte - Männer sind Schweine", false}, songID{"Die Ärzte", "Männer sind Schweine", false}},
		{"artist with a dash in the title", songID{"Jay-Z", "Jay-Z: 99 Problems", false}, songID{"Jay-Z", "99 Problems", false}},
		{"artist in the title, en dash", songID{"Ich & Ich", "Ich & Ich – Vom selben Stern", false}, songID{"Ich & Ich", "Vom selben Stern", false}},
		{"html entity", songID{"DJ Jazzy Jeff &amp; The Fresh Prince", "Summertime", false}, songID{"DJ Jazzy Jeff & The Fresh Prince", "Summertime", false}},
		{"html entity without semicolon", songID{"Simon &amp Garfunkel", "The Boxer", false}, songID{"Simon & Garfunkel", "The Boxer", false}},
		{"kar is noise", songID{"Shearer", "69 (Kar)", false}, songID{"Shearer", "69 [KARAOKE]", false}},
		{"(The) alone is the artist", songID{"(The)", "Song", false}, songID{"The", "Song", false}},
		{"collaboration versus", songID{"A versus B", "Song", false}, songID{"B & A", "Song", false}},
		{"x at the start is part of the name", songID{"X Ambassadors", "Renegades", false}, songID{"XAmbassadors", "Renegades", false}},
		{"x at the end is part of the name", songID{"Malcolm X", "Song", false}, songID{"MalcolmX", "Song", false}},
		{"artist in the title, em dash", songID{"Queen", "Queen — Innuendo", false}, songID{"Queen", "Innuendo", false}},
		{"only the artist and a dash", songID{"James Blunt", "James Blunt -", false}, songID{"James Blunt", "James Blunt", false}},
		{"pv and mv are noise", songID{"Aimer", "Ref:rain (PV)", false}, songID{"Aimer", "Ref:rain [MV]", false}},
		{"self-titled, dash noise", songID{"Black Sabbath", "Black Sabbath - 2009 Remaster", false}, songID{"Black Sabbath", "Black Sabbath", false}},
		{"self-titled, dash live", songID{"Iron Maiden", "Iron Maiden - Live", false}, songID{"Iron Maiden", "Iron Maiden (Live)", false}},
		{"self-titled, dash radio edit", songID{"Iron Maiden", "Iron Maiden - Radio Edit", false}, songID{"Iron Maiden", "Iron Maiden (Radio Edit)", false}},
		{"self-titled, dash duet", songID{"Bad Company", "Bad Company - Duet", false}, songID{"Bad Company", "Bad Company [DUET]", false}},
		{"artist in the title, title starts with a version word", songID{"Billy Joel", "Billy Joel - Piano Man", false}, songID{"Billy Joel", "Piano Man", false}},
		{"artist in the title, radio in the title", songID{"Queen", "Queen - Radio Ga Ga", false}, songID{"Queen", "Radio Ga Ga", false}},
		{"artist in the title, live in the title", songID{"Wings", "Wings - Live and Let Die", false}, songID{"Wings", "Live and Let Die", false}},
		{"artist in the title, a year as the title", songID{"Prince", "Prince - 1999", false}, songID{"Prince", "1999", false}},
		{"artist in the title, a year as the title, the", songID{"The Smashing Pumpkins", "The Smashing Pumpkins - 1979", false}, songID{"The Smashing Pumpkins", "1979", false}},
	}
	for _, tt := range tests {
		ka := SongKey(tt.a.artist, tt.a.title, tt.a.duet)
		kb := SongKey(tt.b.artist, tt.b.title, tt.b.duet)
		if ka == "" || ka != kb {
			t.Errorf("%s: SongKey(%q, %q, %v) = %q, SongKey(%q, %q, %v) = %q, want equal and non-empty",
				tt.name, tt.a.artist, tt.a.title, tt.a.duet, ka, tt.b.artist, tt.b.title, tt.b.duet, kb)
		}
	}
}

func TestSongKeyDistinct(t *testing.T) {
	tests := []struct {
		name string
		a, b songID
	}{
		{"live vs studio", songID{"Artist", "Song (Live)", false}, songID{"Artist", "Song", false}},
		{"live dash vs studio", songID{"Artist", "Song - Live", false}, songID{"Artist", "Song", false}},
		{"acoustic", songID{"Artist", "Song (Acoustic)", false}, songID{"Artist", "Song", false}},
		{"unplugged", songID{"Artist", "Song [Unplugged]", false}, songID{"Artist", "Song", false}},
		{"remix", songID{"Artist", "Song (Remix)", false}, songID{"Artist", "Song", false}},
		{"named remix", songID{"Artist", "Song (DJ X Remix)", false}, songID{"Artist", "Song (DJ Y Remix)", false}},
		{"radio edit", songID{"Artist", "Song (Radio Edit)", false}, songID{"Artist", "Song", false}},
		{"german version", songID{"Artist", "Song (German Version)", false}, songID{"Artist", "Song", false}},
		{"tv", songID{"Artist", "Song (TV)", false}, songID{"Artist", "Song", false}},
		{"version with year", songID{"Artist", "Song (2011 Version)", false}, songID{"Artist", "Song", false}},
		{"taylors version", songID{"Taylor Swift", "Love Story (Taylor's Version)", false}, songID{"Taylor Swift", "Love Story", false}},
		{"remastered live", songID{"Artist", "Song (Remastered Live)", false}, songID{"Artist", "Song", false}},
		{"live remaster vs studio", songID{"Artist", "Song (Live) [Remastered]", false}, songID{"Artist", "Song (Remastered)", false}},
		{"official alone", songID{"Artist", "Song (Official)", false}, songID{"Artist", "Song", false}},
		{"single alone", songID{"Artist", "Song (Single)", false}, songID{"Artist", "Song", false}},
		{"duet vs solo", songID{"Artist", "Song", true}, songID{"Artist", "Song", false}},
		{"duet qualifier vs solo", songID{"Artist", "Song [DUET]", false}, songID{"Artist", "Song", false}},
		{"different artists same title", songID{"Leonard Cohen", "Hallelujah", false}, songID{"Jeff Buckley", "Hallelujah", false}},
		{"different titles", songID{"ABBA", "SOS", false}, songID{"ABBA", "Waterloo", false}},
		{"band named feat", songID{"Little Feat", "Dixie Chicken", false}, songID{"Little", "Dixie Chicken", false}},
		{"other artist joined", songID{"Simon, Garfunkel", "The Boxer", false}, songID{"Simon", "The Boxer", false}},
		{"with is not feat", songID{"Artist", "Song (with X)", false}, songID{"Artist", "Song", false}},
		{"different scripts", songID{"Кино", "Song", false}, songID{"Kino", "Song", false}},
		{"part numbers", songID{"Artist", "Song Part 1", false}, songID{"Artist", "Song Part 2", false}},
		{"x as a name, not a separator", songID{"Malcolm X", "Song", false}, songID{"Malcolm", "Song", false}},
		{"x at the start", songID{"X Ambassadors", "Renegades", false}, songID{"Ambassadors", "Renegades", false}},
		{"a separator joins names", songID{"Simon & Garfunkel", "The Boxer", false}, songID{"Simon", "The Boxer", false}},
		{"the artist only as the title", songID{"James Blunt", "James Blunt", false}, songID{"James Blunt", "", false}},
		{"artist prefix without a dash", songID{"Queen", "Queen Of The Night", false}, songID{"Queen", "Of The Night", false}},
		{"other artist in the title", songID{"Queen", "Bowie - Under Pressure", false}, songID{"Queen", "Under Pressure", false}},
		{"(The) inside a name", songID{"Pinky (The) Brain", "Song", false}, songID{"Pinky Brain", "Song", false}},
		{"artist the only in title", songID{"The The", "Uncertain Smile", false}, songID{"Uncertain", "Smile", false}},
		{"title the kept", songID{"Artist", "The Song", false}, songID{"Artist", "Song", false}},
		{"kana dakuten", songID{"Kenshi", "カゲ", false}, songID{"Kenshi", "カケ", false}},
		{"kana handakuten", songID{"Kenshi", "パン", false}, songID{"Kenshi", "ハン", false}},
		{"cyrillic short i", songID{"Kino", "Мой", false}, songID{"Kino", "Мои", false}},
		{"cyrillic yo", songID{"Artist", "Ёлка", false}, songID{"Artist", "Елка", false}},
		{"thai vowel and tone marks", songID{"Artist", "ทั้ง", false}, songID{"Artist", "ทง", false}},
		{"devanagari vowel signs", songID{"Artist", "नमस्ते", false}, songID{"Artist", "नमस ते", false}},
		{"feat then dash live", songID{"Artist", "Song feat. X - Live", false}, songID{"Artist", "Song", false}},
		{"feat then remix", songID{"Artist", "Song feat. X Remix", false}, songID{"Artist", "Song", false}},
		{"ft then remix", songID{"Artist", "Song ft. X Remix", false}, songID{"Artist", "Song", false}},
		{"feat bracket with live", songID{"Artist", "Song (feat. X Live)", false}, songID{"Artist", "Song", false}},
		{"featuring bracket with remix", songID{"Artist", "Song (Featuring X Remix)", false}, songID{"Artist", "Song", false}},
		{"ft mix bracket", songID{"Artist", "Song (Ft Mix)", false}, songID{"Artist", "Song", false}},
		{"ft abbreviation in a title", songID{"Artist", "Welcome to Ft. Lauderdale", false}, songID{"Artist", "Welcome to", false}},
		{"duet acoustic vs duet", songID{"Artist", "Song [Duet - Acoustic]", false}, songID{"Artist", "Song [Duet]", false}},
		{"duet remix vs duet", songID{"Artist", "Song (Duet Remix)", false}, songID{"Artist", "Song [Duet]", false}},
		{"duet with names live vs duet", songID{"Artist", "Song (Duet with B, Live)", false}, songID{"Artist", "Song [Duet]", false}},
		{"self-titled live vs studio", songID{"Iron Maiden", "Iron Maiden - Live", false}, songID{"Iron Maiden", "Iron Maiden", false}},
	}
	for _, tt := range tests {
		ka := SongKey(tt.a.artist, tt.a.title, tt.a.duet)
		kb := SongKey(tt.b.artist, tt.b.title, tt.b.duet)
		if ka == kb {
			t.Errorf("%s: SongKey(%q, %q, %v) == SongKey(%q, %q, %v) = %q, want different",
				tt.name, tt.a.artist, tt.a.title, tt.a.duet, tt.b.artist, tt.b.title, tt.b.duet, ka)
		}
	}
}

func TestSongKeyEmpty(t *testing.T) {
	tests := []songID{
		{"", "Song", false},
		{"Artist", "", false},
		{"   ", "Song", false},
		{"!!!", "Song", false},
		{"Artist", "???", false},
		{"Artist", "[DUET]", false},
		{"Artist", "(Remastered 2011)", false},
		{"Artist", "(feat. X)", false},
		{"(feat. X)", "Song", false},
	}
	for _, tt := range tests {
		if got := SongKey(tt.artist, tt.title, tt.duet); got != "" {
			t.Errorf("SongKey(%q, %q, %v) = %q, want empty", tt.artist, tt.title, tt.duet, got)
		}
	}
	// Edge cases that must keep a key.
	keep := []songID{
		{"The", "Song", false},            // "The" alone is the artist
		{"Artist", "Feat", false},         // a title that is the word "feat"
		{"Little Feat", "Willin'", false}, // band name ends with "feat"
		{"Artist", "Duet", false},         // a title, not a qualifier
		{"Artist", "(Live)", false},       // leading bracket is part of the name
		{"Artist", "1984", false},         // a year without brackets is a title
	}
	for _, tt := range keep {
		if got := SongKey(tt.artist, tt.title, tt.duet); got == "" {
			t.Errorf("SongKey(%q, %q, %v) is empty", tt.artist, tt.title, tt.duet)
		}
	}
}

func TestSongKeyFormat(t *testing.T) {
	// The exact format is internal, but it must stay stable within a run and readable in
	// debug logs.
	tests := []struct {
		id   songID
		want string
	}{
		{songID{"The Beatles", "Help!", false}, "beatles|help"},
		{songID{"Beyoncé feat. Jay-Z", "Crazy in Love (Remastered)", false}, "beyonce|crazyinlove"},
		{songID{"Artist", "Song [DUET]", false}, "artist|song|duet"},
		{songID{"Artist", "Song (Live)", true}, "artist|songlive|duet"},
		{songID{"Simon & Garfunkel", "Mrs. Robinson", false}, "garfunkel&simon|mrsrobinson"},
		{songID{"(hed) p.e.", "Bartender", false}, "hedpe|bartender"},
		{songID{"Artist", "Song [Duet - Acoustic]", false}, "artist|songacoustic|duet"},
		{songID{"Artist", "Song (feat. X Live)", false}, "artist|songxlive"},
		{songID{"Kino", "Мой", false}, "kino|мой"},
	}
	for _, tt := range tests {
		if got := SongKey(tt.id.artist, tt.id.title, tt.id.duet); got != tt.want {
			t.Errorf("SongKey(%q, %q, %v) = %q, want %q", tt.id.artist, tt.id.title, tt.id.duet, got, tt.want)
		}
	}
}

func TestLooseKey(t *testing.T) {
	equal := [][2]songID{
		{{"Artist", "Song (Live)", false}, {"Artist", "Song", false}},
		{{"Artist", "Song [Acoustic]", false}, {"Artist", "Song (Remix)", false}},
		{{"Artist", "Song - Radio Edit", false}, {"Artist", "Song", false}},
		{{"Artist", "Song [DUET]", false}, {"Artist", "Song", false}},
		{{"Artist", "Song (German Version)", false}, {"Artist", "Song", false}},
		{{"The Artist feat. X", "Song (feat. Y)", false}, {"Artist", "Song", false}},
		{{"Beyoncé", "Halo (Live) [HD]", false}, {"Beyonce", "Halo", false}},
		{{"Artist", "Song ft. X Remix", false}, {"Artist", "Song", false}},
		{{"Artist", "Song (feat. X Live)", false}, {"Artist", "Song", false}},
		{{"Artist", "Song [Duet - Acoustic]", false}, {"Artist", "Song", false}},
		{{"Oasis", "Wonder\u00adwall", false}, {"Oasis", "Wonderwall", false}},
		{{"Black Sabbath", "Black Sabbath - 2009 Remaster", false}, {"Black Sabbath", "Black Sabbath", false}},
	}
	for _, p := range equal {
		ka, kb := LooseKey(p[0].artist, p[0].title), LooseKey(p[1].artist, p[1].title)
		if ka == "" || ka != kb {
			t.Errorf("LooseKey(%q, %q) = %q, LooseKey(%q, %q) = %q, want equal", p[0].artist, p[0].title, ka, p[1].artist, p[1].title, kb)
		}
	}
	distinct := [][2]songID{
		{{"Leonard Cohen", "Hallelujah", false}, {"Jeff Buckley", "Hallelujah", false}},
		{{"Artist", "Song", false}, {"Artist", "Other Song", false}},
		// A leading bracket group is part of the name, not a qualifier.
		{{"The Rolling Stones", "(I Can't Get No) Satisfaction", false}, {"The Rolling Stones", "Satisfaction", false}},
		{{"Kino", "Мой", false}, {"Kino", "Мои", false}},
		{{"Kenshi", "カゲ", false}, {"Kenshi", "カケ", false}},
	}
	for _, p := range distinct {
		ka, kb := LooseKey(p[0].artist, p[0].title), LooseKey(p[1].artist, p[1].title)
		if ka == kb {
			t.Errorf("LooseKey(%q, %q) == LooseKey(%q, %q) = %q, want different", p[0].artist, p[0].title, p[1].artist, p[1].title, ka)
		}
	}
	if got := LooseKey("Artist", "[Live]  "); got == "" {
		// A leading group is kept, so this title is not empty.
		t.Errorf("LooseKey with a leading group is empty")
	}
	if got := LooseKey("", "Song"); got != "" {
		t.Errorf("LooseKey without artist = %q, want empty", got)
	}
	// Loose keys never mark duets: a strict duet key differs from its solo key, the loose
	// keys match.
	if SongKey("A", "S [DUET]", false) == SongKey("A", "S", false) || LooseKey("A", "S [DUET]") != LooseKey("A", "S") {
		t.Errorf("duet handling of SongKey/LooseKey is wrong")
	}
}

func TestIsDuetTitle(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Song [DUET]", true},
		{"Song [Duet]", true},
		{"Song (duet)", true},
		{"Song (Duet Version)", true},
		{"Song (Duett)", true},
		{"Song (Duett mit Peter Maffay)", true},
		{"Song (Duet with Elton John)", true},
		{"Song {DUET}", true},
		{"[DUET] Song", true},
		{"Song - Duet", true},
		{"Song – Duet", true},
		{"Song [DUET] (Live)", true},
		{"Song [Duet - Acoustic]", true},
		{"Song (Duet with B, Live)", true},
		{"Song - Duet - Live", true},
		{"Song (Live) [DUET]", true},
		{"Song", false},
		{"Duet", false},
		{"Duet for One", false},
		{"Song (Live)", false},
		{"Song (A Duet Story)", false},
		{"Duets", false},
		{"Song [Duets]", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsDuetTitle(tt.title); got != tt.want {
			t.Errorf("IsDuetTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestClassifyQualifier(t *testing.T) {
	tests := []struct {
		q    string
		want qualifierKind
	}{
		{"Remastered", qualNoise},
		{"Remastered 2011", qualNoise},
		{"2011 Remaster", qualNoise},
		{"Official Music Video", qualNoise},
		{"Lyric Video", qualNoise},
		{"Official Audio", qualNoise},
		{"1984", qualNoise},
		{"Album Version", qualNoise},
		{"Live", qualOther},
		{"Official", qualOther},
		{"Version", qualOther},
		{"2011 Version", qualOther},
		{"Album", qualOther},
		{"Live Video", qualOther},
		{"1984 Live", qualOther},
		{"DUET", qualDuet},
		{"feat. X", qualFeat},
		{"ft X", qualFeat},
		{"Featuring X", qualFeat},
		{"feat. X Live", qualOther},
		{"Ft Mix", qualOther},
		{"Duet Remix", qualDuet},
	}
	for _, tt := range tests {
		if got := classifyQualifier(normWords(tt.q)); got != tt.want {
			t.Errorf("classifyQualifier(%q) = %v, want %v", tt.q, got, tt.want)
		}
	}
}
