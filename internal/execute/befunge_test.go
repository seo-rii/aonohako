package execute

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runBefunge(t *testing.T, program, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	src := filepath.Join(t.TempDir(), "Main.bf93")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write program: %v", err)
	}
	cmd := exec.Command(python, filepath.Join("..", "..", "scripts", "befunge.py"), src)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run befunge: %v", runErr)
		}
	}
	return out.String(), errBuf.String(), code
}

func TestBefungeArithmeticAndOutput(t *testing.T) {
	// `.` prints the popped integer followed by a space.
	if out, e, c := runBefunge(t, "1.@", ""); c != 0 || out != "1 " {
		t.Fatalf("1.@: out=%q exit=%d stderr=%q", out, c, e)
	}
	// 9*9 = 81.
	if out, e, c := runBefunge(t, "99*.@", ""); c != 0 || out != "81 " {
		t.Fatalf("99*.@: out=%q exit=%d stderr=%q", out, c, e)
	}
}

func TestBefungeStringModeAndCharOutput(t *testing.T) {
	// `,` prints a character; the string is pushed reversed so H is on top.
	if out, e, c := runBefunge(t, `"A",@`, ""); c != 0 || out != "A" {
		t.Fatalf("char out: out=%q exit=%d stderr=%q", out, c, e)
	}
	if out, e, c := runBefunge(t, `"olleH",,,,,@`, ""); c != 0 || out != "Hello" {
		t.Fatalf("string out: out=%q exit=%d stderr=%q", out, c, e)
	}
}

func TestBefungeInput(t *testing.T) {
	// `&` reads an integer from input.
	if out, e, c := runBefunge(t, "&.@", "42"); c != 0 || out != "42 " {
		t.Fatalf("& input: out=%q exit=%d stderr=%q", out, c, e)
	}
	// `~` reads a byte; at EOF it pushes -1.
	if out, e, c := runBefunge(t, "~.@", ""); c != 0 || out != "-1 " {
		t.Fatalf("~ eof: out=%q exit=%d stderr=%q", out, c, e)
	}
}

// Division truncates toward zero, as in the reference interpreter, while
// retaining integer precision for operands that cannot be represented as floats.
func TestBefungeDivisionTruncatesTowardZero(t *testing.T) {
	cases := []struct {
		name  string
		prog  string
		stdin string
		want  string
	}{
		{"positive operands", "72/.@", "", "3 "},
		{"negative dividend", "07-2/.@", "", "-3 "},
		{"negative divisor", "702-/.@", "", "-3 "},
		{"both operands negative", "07-02-/.@", "", "3 "},
		{"negative fraction", "01-2/.@", "", "0 "},
		{"negative divisor fraction", "102-/.@", "", "0 "},
		{"exact negative quotient", "08-2/.@", "", "-4 "},
		{"zero dividend", "02/.@", "", "0 "},
		{"zero divisor", "70/.@", "", "0 "},
		{"integer beyond float precision", "&&/.@", "9007199254740993 1", "9007199254740993 "},
		{"large positive quotient", "&&/.@", "18014398509481987 2", "9007199254740993 "},
		{"large negative dividend", "&&/.@", "-18014398509481987 2", "-9007199254740993 "},
		{"large negative divisor", "&&/.@", "18014398509481987 -2", "-9007199254740993 "},
		{"large negative operands", "&&/.@", "-18014398509481987 -2", "9007199254740993 "},
		{"modulo retains existing behavior", "07-2%.@", "", "1 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, code := runBefunge(t, tc.prog, tc.stdin)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, errOut)
			}
			if out != tc.want {
				t.Fatalf("%s = %q, want %q", tc.prog, out, tc.want)
			}
		})
	}
}

// TestBefungeInputSharesOneCursorAcrossNumberAndChar pins that `&` and `~` read
// from a single shared input cursor. Previously they tracked independent
// positions, so a `~` after a `&` re-read bytes the `&` had already consumed.
func TestBefungeInputSharesOneCursorAcrossNumberAndChar(t *testing.T) {
	// `&` consumes "5"; `~` must then read 'A' (0x41 = 65), not re-read '5'.
	// Output prints the char code (top) then the number: "65 5 ".
	if out, e, c := runBefunge(t, "&~..@", "5A"); c != 0 || out != "65 5 " {
		t.Fatalf("&~ shared cursor: out=%q exit=%d stderr=%q", out, c, e)
	}
	// Two consecutive `&` reads advance past both integers.
	if out, e, c := runBefunge(t, "&&..@", "12 34"); c != 0 || out != "34 12 " {
		t.Fatalf("&& sequential reads: out=%q exit=%d stderr=%q", out, c, e)
	}
}
