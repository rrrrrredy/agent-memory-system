package episodes

import (
	"reflect"
	"testing"
)

func TestSplitClausesPreservesDottedTechnicalTokens(t *testing.T) {
	input := "Please remember that cache files belong under .cache/aurora. " +
		"Daily exports use atlas-YYYYMMDD.json. Continue offline!"
	want := []string{
		"Please remember that cache files belong under .cache/aurora",
		"Daily exports use atlas-YYYYMMDD.json",
		"Continue offline",
	}
	if got := splitClauses(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("dotted technical tokens were not preserved:\nwant=%#v\ngot=%#v", want, got)
	}
}
