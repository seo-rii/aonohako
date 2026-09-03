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
