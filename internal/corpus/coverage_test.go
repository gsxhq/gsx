package corpus

import "testing"

func TestCoverageReport(t *testing.T) {
	cases := []*caseDoc{
		{name: "b/two", goldens: map[string][]byte{"diagnostics.golden": {}, "render.golden": []byte("x")}, invoke: []byte("X()")},
		{name: "a/one", goldens: map[string][]byte{"diagnostics.golden": []byte("err")}},
	}
	want := "a/one\tdiag(error)\nb/two\tdiag render\nTOTAL: 2 cases (render: 1, error: 1, gen-pinned: 0)\n"
	if got := string(coverageReport(cases)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
