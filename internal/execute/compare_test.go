package execute

import "testing"

// TestCompareOutputsVerdictSemantics pins the AC/WA line comparison the judge
// uses when no SPJ or interactor is present. compareOutputs trims trailing
// whitespace per line and ignores trailing blank lines, but is otherwise exact.
func TestCompareOutputsVerdictSemantics(t *testing.T) {
	cases := []struct {
		name             string
		expected, actual string
		want             bool
	}{
		{"identical", "abc\n", "abc\n", true},
		{"trailing spaces per line ignored", "a b\nc\n", "a b   \nc\t\n", true},
		{"crlf vs lf", "a\nb\n", "a\r\nb\r\n", true},
		{"trailing blank lines ignored", "a\nb", "a\nb\n\n\n", true},
		{"missing final newline still matches", "a\nb\n", "a\nb", true},
		{"leading whitespace is significant", "a\n", " a\n", false},
		{"internal difference", "hello\n", "hallo\n", false},
		{"extra content line", "a\n", "a\nb\n", false},
		{"both empty", "", "", true},
		{"empty vs only blank lines", "", "\n\n", true},
		{"empty vs content", "", "x\n", false},
		{"unicode identical", "héllo\n", "héllo\n", true},
		{"blank line in the middle is significant", "a\n\nb\n", "a\nb\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareOutputs([]byte(tc.expected), []byte(tc.actual)); got != tc.want {
				t.Fatalf("compareOutputs(%q, %q) = %v, want %v", tc.expected, tc.actual, got, tc.want)
			}
		})
	}
}

// FuzzCompareOutputs asserts the comparator never panics and stays reflexive and
// symmetric on arbitrary bytes, so a submission can never be judged against its
// reference in an order-dependent or crashing way.
func FuzzCompareOutputs(f *testing.F) {
	f.Add([]byte("a\nb\n"), []byte("a \nb\n\n"))
	f.Add([]byte(""), []byte("\n"))
	f.Add([]byte("x"), []byte("x"))
	f.Add([]byte("a\r\nb"), []byte("a\nb\n"))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		if !compareOutputs(a, a) {
			t.Fatalf("compareOutputs not reflexive for %q", a)
		}
		if compareOutputs(a, b) != compareOutputs(b, a) {
			t.Fatalf("compareOutputs not symmetric for %q vs %q", a, b)
		}
	})
}
