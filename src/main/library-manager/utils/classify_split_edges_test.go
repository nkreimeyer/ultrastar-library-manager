package utils

import (
	"reflect"
	"testing"
)

// An isolated CP1252 "ß…" (0xDF 0xE2...) style sequence is accidental, not evidence.
func TestClassifyUTF8SequencesCountsAccidental(t *testing.T) {
	// " \xDF\xBF " is an isolated 2-byte sequence with lead 0xDF (U+07FF).
	acc, ev := classifyUTF8Sequences([]byte("a \xDF\xBF b \xDF\xBF c"))
	if acc != 2 || ev != 0 {
		t.Errorf("accidental, evidence = %d, %d; want 2, 0", acc, ev)
	}
	acc, ev = classifyUTF8Sequences([]byte("a \xC3\xA4 b"))
	if acc != 0 || ev != 1 {
		t.Errorf("accidental, evidence = %d, %d; want 0, 1", acc, ev)
	}
}

// splitWords never yields empty words, also not for leading or repeated separators.
func TestSplitWordsNoEmptyWords(t *testing.T) {
	got := splitWords("  ab  cd ")
	if want := []string{"ab", "cd"}; !reflect.DeepEqual(got, want) {
		t.Errorf("splitWords = %q, want %q", got, want)
	}
	if got := splitWords("   "); len(got) != 0 {
		t.Errorf("splitWords of blanks = %q", got)
	}
}

// A separator at the very end of the name is still a split point.
func TestSplitFolderNameSeparatorAtEnd(t *testing.T) {
	got := splitFolderName("A" + folderNameSep)
	if len(got) != 1 || got[0].left != "A" || got[0].right != "" {
		t.Errorf("splitFolderName = %+v", got)
	}
	if got := splitFolderName(folderNameSep); len(got) != 1 {
		t.Errorf("splitFolderName(sep only) = %+v", got)
	}
}
