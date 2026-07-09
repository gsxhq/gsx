package corpus

import (
	"flag"
	"reflect"
	"testing"
)

func TestSplitRunPattern(t *testing.T) {
	t.Parallel()

	got := splitRunPattern(`TestCorpus/attrs\/spread_byo/[ab/c]`)
	want := []string{"TestCorpus", `attrs\/spread_byo`, "[ab/c]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitRunPattern mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSelectedCaseNamesForRun(t *testing.T) {
	cases := []*caseDoc{
		{name: "attrs/spread_byo"},
		{name: "attrs/expr_attrs"},
		{name: "props/splat_composition"},
	}

	runFlag := flag.Lookup("test.run")
	if runFlag == nil {
		t.Fatal("missing test.run flag")
	}
	orig := runFlag.Value.String()
	t.Cleanup(func() {
		_ = flag.Set("test.run", orig)
	})

	mustSet := func(v string) {
		t.Helper()
		if err := flag.Set("test.run", v); err != nil {
			t.Fatalf("set test.run: %v", err)
		}
	}

	mustSet("TestCorpus/attrs/spread_byo")
	got := selectedCaseNamesForRun("TestCorpus", cases)
	if !reflect.DeepEqual(got, map[string]bool{"attrs/spread_byo": true}) {
		t.Fatalf("exact match mismatch: %#v", got)
	}

	mustSet("TestCorpus/attrs")
	got = selectedCaseNamesForRun("TestCorpus", cases)
	want := map[string]bool{
		"attrs/spread_byo": true,
		"attrs/expr_attrs": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segment match mismatch\n got: %#v\nwant: %#v", got, want)
	}

	mustSet("TestExamples/attrs")
	if got = selectedCaseNamesForRun("TestCorpus", cases); got != nil {
		t.Fatalf("top-level mismatch should return nil, got: %#v", got)
	}
}
