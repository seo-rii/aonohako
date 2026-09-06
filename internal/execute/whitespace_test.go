package execute

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Whitespace instruction primitives. Bit " " = 0, "\t" = 1.
const (
	wsAdd      = "\t   "  // [TAB SPACE] arith, [SPACE SPACE] add
	wsSub      = "\t  \t" // ... [SPACE TAB] sub
	wsMul      = "\t  \n" // ... [SPACE LF] mul
	wsDiv      = "\t \t "  // ... [TAB SPACE] div
	wsMod      = "\t \t\t" // ... [TAB TAB] mod
	wsStore    = "\t\t "   // [TAB TAB] heap, [SPACE] store
	wsRetrieve = "\t\t\t"  // [TAB TAB] heap, [TAB] retrieve
	wsOutNum   = "\t\n \t" // [TAB LF] io, [SPACE TAB] out_num
	wsOutChar  = "\t\n  "  // [TAB LF] io, [SPACE SPACE] out_char
	wsReadNum  = "\t\n\t\t" // [TAB LF] io, [TAB TAB] read_num (into heap[addr])
	wsEnd      = "\n\n\n"   // [LF LF LF] terminate
)

func wsPush(n int64) string {
	sign := " "
	magnitude := n
	if n < 0 {
		sign = "\t"
		magnitude = -n
	}
	var bits strings.Builder
	if magnitude == 0 {
		bits.WriteString(" ")
	} else {
		for _, c := range strconv.FormatInt(magnitude, 2) {
			if c == '1' {
				bits.WriteString("\t")
			} else {
				bits.WriteString(" ")
			}
		}
	}
	return "  " + sign + bits.String() + "\n" // [SPACE SPACE] push, sign, bits, LF
}

func runWhitespace(t *testing.T, program, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	src := filepath.Join(t.TempDir(), "Main.ws")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write program: %v", err)
	}
	cmd := exec.Command(python, filepath.Join("..", "..", "scripts", "whitespace.py"), src)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run whitespace: %v", runErr)
		}
	}
	return out.String(), errBuf.String(), code
}

// TestWhitespaceFloorDivisionRegression guards the div opcode, which previously
// used float division (int(a / b)): it truncated toward zero and lost precision
// above 2^53. It must now floor and stay exact for large operands.
func TestWhitespaceFloorDivisionRegression(t *testing.T) {
	cases := []struct {
		name string
		a, b int64
		want string
	}{
		{"positive", 7, 2, "3"},
		{"negative dividend floors toward minus infinity", -7, 2, "-4"},
		{"large dividend stays exact", 1000000000000000005, 2, "500000000000000002"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog := wsPush(tc.a) + wsPush(tc.b) + wsDiv + wsOutNum + wsEnd
			out, errOut, code := runWhitespace(t, prog, "")
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, errOut)
			}
			if out != tc.want {
				t.Fatalf("%d // %d = %q, want %q", tc.a, tc.b, out, tc.want)
			}
		})
	}
}

// TestWhitespaceModIsFloorModulo pins mod alongside div so the language keeps
// the a == (a//b)*b + (a%b) identity: (-7 // 2)*2 + (-7 mod 2) == -8 + 1 == -7.
func TestWhitespaceModIsFloorModulo(t *testing.T) {
	prog := wsPush(-7) + wsPush(2) + wsMod + wsOutNum + wsEnd
	out, errOut, code := runWhitespace(t, prog, "")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if out != "1" {
		t.Fatalf("-7 mod 2 = %q, want %q", out, "1")
	}
}

func TestWhitespaceArithmeticAndOutput(t *testing.T) {
	if out, e, c := runWhitespace(t, wsPush(40)+wsPush(2)+wsAdd+wsOutNum+wsEnd, ""); c != 0 || out != "42" {
		t.Fatalf("40+2: out=%q exit=%d stderr=%q", out, c, e)
	}
	if out, e, c := runWhitespace(t, wsPush(50)+wsPush(8)+wsSub+wsOutNum+wsEnd, ""); c != 0 || out != "42" {
		t.Fatalf("50-8: out=%q exit=%d stderr=%q", out, c, e)
	}
	if out, e, c := runWhitespace(t, wsPush('A')+wsOutChar+wsEnd, ""); c != 0 || out != "A" {
		t.Fatalf("out_char('A'): out=%q exit=%d stderr=%q", out, c, e)
	}
}

// TestWhitespaceReadsNumberFromStdin exercises read_num -> heap store -> retrieve
// -> out_num, echoing a number provided on stdin.
func TestWhitespaceReadsNumberFromStdin(t *testing.T) {
	prog := wsPush(0) + wsReadNum + wsPush(0) + wsRetrieve + wsOutNum + wsEnd
	if out, e, c := runWhitespace(t, prog, "123\n"); c != 0 || out != "123" {
		t.Fatalf("read_num echo: out=%q exit=%d stderr=%q", out, c, e)
	}
}
