package parser

import "testing"

func TestScanGoElementMarks(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []int // byte offsets of element-starting '<'
	}{
		// --- elements at operand positions ---
		{"assign", `x = <div/>`, []int{4}},
		{"define", `x := <Foo/>`, []int{5}},
		{"return", `return <div/>`, []int{7}},
		{"call arg", `f(<Foo/>)`, []int{2}},
		{"second call arg", `f(a, <Foo/>)`, []int{5}},
		// NOTE: the task brief's table has this as []int{4, 9}, but the
		// second '<' in `[]T{<a/>, <b/>}` is genuinely at byte offset 10
		// (count: '[',']','T','{','<','a','/','>',',',' ','<' -> index 10),
		// not 9. Verified with a byte-offset dump; flagged in the task
		// report rather than silently "corrected" without explanation.
		{"slice elem", `[]T{<a/>, <b/>}`, []int{4, 10}},
		{"composite value", `M{K: <Foo/>}`, []int{5}},
		{"paren", `(<div/>)`, []int{1}},
		{"unary not", `!<Foo/>`, []int{1}}, // nonsensical but position-correct
		{"binary rhs", `x && <Foo/>`, []int{5}},

		// --- NOT elements: '<' in operator position is less-than ---
		{"less than", `a < b`, nil},
		{"less no space", `a<b`, nil},
		{"lte", `a <= b`, nil},
		{"shift", `a << b`, nil},
		{"cmp chain", `a < b && c > d`, nil},
		{"index cmp", `arr[i] < n`, nil},
		{"call result cmp", `f(x) < g(y)`, nil},

		// --- NOT elements: channel ops ---
		{"chan recv", `x := <-ch`, nil},
		{"chan send", `ch <- x`, nil},
		{"recv in call", `f(<-ch)`, nil},

		// --- Go generics use [] not <> : no ambiguity ---
		{"generic call", `Map[int, string](m)`, nil},
		{"generic decl frag", `[]Pair[K, V]{}`, nil},

		// --- element with nested Go / attrs / children ---
		{"attrs+interp", `x = <a href={u} class="c">{ label }</a>`, []int{4}},
		{"nested tag not counted twice", `x = <div><span/></div>`, []int{4}}, // outer only; inner is inside the element span
		{"lt after element", `<Foo/> < 3`, []int{0}},                         // element first, then a real '<'
		// Regression: text content is prose, not Go source. The lone
		// apostrophe in "it's" and the "http://" URL must NOT be lexed as a
		// rune literal / line comment by the span walk — otherwise the skip
		// runs to EOF and the sibling <b/> is silently dropped.
		{"text apostrophe and url then sibling", `x = <a href="/help">it's here: http://x</a>, <b/>`, []int{4, 45}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			marks := scanGoElementMarks(c.src)
			got := make([]int, len(marks))
			for i, m := range marks {
				got[i] = m.Off
			}
			if !equalInts(got, c.want) {
				t.Fatalf("scanGoElementMarks(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
