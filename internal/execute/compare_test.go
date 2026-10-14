package execute

import (
	"bytes"
	"fmt"
	"testing"
)

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
		{"leading blank line is significant", "\na\n", "a\n", false},
		{"whitespace-only middle line", "a\n\nb", "a\n \t\r\nb", true},
		{"empty vs trailing crlf tabs", "", " \t\r\n\t\r\n", true},
		{"vertical tab is significant", "a\n", "a\v\n", false},
		{"form feed is significant", "a\n", "a\f\n", false},
		{"nonbreaking space is significant", "a\n", "a\u00a0\n", false},
		{"nul bytes preserved", "a\x00b\n", "a\x00b\t\r\n", true},
		{"nul byte differs", "a\x00b", "ab", false},
		{"invalid utf8 preserved", "\xff\x80\xfe\n", "\xff\x80\xfe \t\r\n", true},
		{"invalid utf8 differs", "\xff\x80", "\xfe\x80", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareOutputs([]byte(tc.expected), []byte(tc.actual)); got != tc.want {
				t.Fatalf("compareOutputs(%q, %q) = %v, want %v", tc.expected, tc.actual, got, tc.want)
			}
		})
	}
}

func TestCompareOutputsLargeInputs(t *testing.T) {
	longLine := bytes.Repeat([]byte{'x'}, 4<<20)
	cases := []struct {
		name             string
		expected, actual []byte
		want             bool
	}{
		{"only newlines", nil, bytes.Repeat([]byte{'\n'}, 4<<20), true},
		{"only crlf and tabs", nil, bytes.Repeat([]byte(" \t\r\n"), 1<<20), true},
		{"long line matches", longLine, append(bytes.Clone(longLine), ' ', '\t', '\r', '\n'), true},
		{"long line differs", longLine, append(bytes.Clone(longLine), 'y'), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareOutputs(tc.expected, tc.actual); got != tc.want {
				t.Fatalf("compareOutputs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompareOutputsNoAllocations(t *testing.T) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20} {
		t.Run(fmt.Sprintf("%d_bytes", size), func(t *testing.T) {
			blank := bytes.Repeat([]byte{'\n'}, size)
			manyLines := bytes.Repeat([]byte("x\n"), size/2)
			for _, input := range [][]byte{blank, manyLines} {
				allocations := testing.AllocsPerRun(1, func() {
					if !compareOutputs(input, input) {
						panic("comparison must be reflexive")
					}
				})
				if allocations != 0 {
					t.Fatalf("comparison allocated %v times for %d bytes", allocations, size)
				}
			}
		})
	}
}

// compareOutputsReference preserves the original span-based algorithm as a
// differential oracle. Its allocations are acceptable only in small fuzz cases.
func compareOutputsReference(expected, actual []byte) bool {
	type lineSpan struct{ start, end int }
	split := func(input []byte) []lineSpan {
		var lines []lineSpan
		for start := 0; start <= len(input); {
			newline := bytes.IndexByte(input[start:], '\n')
			end := len(input)
			if newline >= 0 {
				end = start + newline
			}
			trimmed := end
			for trimmed > start && (input[trimmed-1] == ' ' || input[trimmed-1] == '\t' || input[trimmed-1] == '\r') {
				trimmed--
			}
			lines = append(lines, lineSpan{start, trimmed})
			if newline < 0 {
				break
			}
			start = end + 1
		}
		for len(lines) > 0 && lines[len(lines)-1].start == lines[len(lines)-1].end {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	expectedLines, actualLines := split(expected), split(actual)
	if len(expectedLines) != len(actualLines) {
		return false
	}
	for i, expectedLine := range expectedLines {
		actualLine := actualLines[i]
		if !bytes.Equal(expected[expectedLine.start:expectedLine.end], actual[actualLine.start:actualLine.end]) {
			return false
		}
	}
	return true
}

// FuzzCompareOutputs keeps the byte-level verdict contract identical to the
// original implementation as well as checking reflexivity and symmetry.
func FuzzCompareOutputs(f *testing.F) {
	f.Add([]byte("a\nb\n"), []byte("a \nb\n\n"))
	f.Add([]byte(""), []byte("\n"))
	f.Add([]byte("x"), []byte("x"))
	f.Add([]byte("a\r\nb"), []byte("a\nb\n"))
	f.Add([]byte("a\n \t\r\nb\n"), []byte("a\n\nb\n\n"))
	f.Add([]byte("\x00\xff\x80\n"), []byte("\x00\xff\x80\t\r\n"))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		if len(a) > 64<<10 || len(b) > 64<<10 {
			t.Skip("bound the allocating reference implementation")
		}
		if got, want := compareOutputs(a, b), compareOutputsReference(a, b); got != want {
			t.Fatalf("compareOutputs(%q, %q) = %v, reference = %v", a, b, got, want)
		}
		if !compareOutputs(a, a) {
			t.Fatalf("compareOutputs not reflexive for %q", a)
		}
		if compareOutputs(a, b) != compareOutputs(b, a) {
			t.Fatalf("compareOutputs not symmetric for %q vs %q", a, b)
		}
	})
}

func BenchmarkCompareOutputs(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20} {
		for _, shape := range []string{"newlines", "content_lines", "single_line"} {
			b.Run(fmt.Sprintf("%s/%d_bytes", shape, size), func(b *testing.B) {
				var expected, actual []byte
				switch shape {
				case "newlines":
					actual = bytes.Repeat([]byte{'\n'}, size)
				case "content_lines":
					expected = bytes.Repeat([]byte("x\n"), size/2)
					actual = expected
				case "single_line":
					expected = bytes.Repeat([]byte{'x'}, size)
					actual = expected
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(expected) + len(actual)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if !compareOutputs(expected, actual) {
						b.Fatal("equivalent outputs did not match")
					}
				}
			})
		}
	}
}
