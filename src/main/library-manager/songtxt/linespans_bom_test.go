package songtxt

import (
	"reflect"
	"testing"
)

// A source that is only a BOM has one empty line after the BOM.
func TestLineSpansBOMOnly(t *testing.T) {
	got := LineSpans([]byte{0xEF, 0xBB, 0xBF})
	if want := [][2]int{{3, 3}}; !reflect.DeepEqual(got, want) {
		t.Errorf("LineSpans(BOM) = %v, want %v", got, want)
	}
}
