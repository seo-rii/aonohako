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

func runBrainfuck(t *testing.T, program, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	src := filepath.Join(t.TempDir(), "Main.bf")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write program: %v", err)
	}
	cmd := exec.Command(python, filepath.Join("..", "..", "scripts", "brainfuck.py"), src)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run brainfuck: %v", runErr)
		}
	}
	return out.String(), errBuf.String(), code
}

func TestBrainfuckHelloWorld(t *testing.T) {
	const hello = "++++++++[>++++[>++>+++>+++>+<<<<-]>+>+>->>+[<]<-]>>.>---.+++++++..+++.>>.<-.<.+++.------.--------.>>+.>++."
	out, errOut, code := runBrainfuck(t, hello, "")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if out != "Hello World!\n" {
		t.Fatalf("hello world output = %q, want %q", out, "Hello World!\n")
	}
}

// TestBrainfuckCellWraparoundAndEOF pins the two behaviors most implementations
// get wrong: 8-bit cell wraparound and the EOF-reads-zero convention.
func TestBrainfuckCellWraparoundAndEOF(t *testing.T) {
	// Cell 0 decremented underflows to 255 (0xFF).
	if out, e, c := runBrainfuck(t, "-.", ""); c != 0 || out != "\xff" {
		t.Fatalf("underflow: out=%x exit=%d stderr=%q", out, c, e)
	}
	// 256 increments wrap back to 0 (0x00).
	if out, e, c := runBrainfuck(t, strings.Repeat("+", 256)+".", ""); c != 0 || out != "\x00" {
		t.Fatalf("wraparound: out=%x exit=%d stderr=%q", out, c, e)
	}
	// Reading input at EOF yields 0.
	if out, e, c := runBrainfuck(t, ",.", ""); c != 0 || out != "\x00" {
		t.Fatalf("eof read: out=%x exit=%d stderr=%q", out, c, e)
	}
}

func TestBrainfuckEchoesInputUntilEOF(t *testing.T) {
	// ,[.,] copies stdin to stdout until a 0 byte (here, EOF) is read.
	if out, e, c := runBrainfuck(t, ",[.,]", "Hi!"); c != 0 || out != "Hi!" {
		t.Fatalf("echo: out=%q exit=%d stderr=%q", out, c, e)
	}
}

func TestBrainfuckUnmatchedBracketFails(t *testing.T) {
	_, errOut, code := runBrainfuck(t, "+[+", "")
	if code == 0 {
		t.Fatalf("unmatched bracket should fail, got exit 0")
	}
	if !strings.Contains(strings.ToLower(errOut), "unmatched") {
		t.Fatalf("stderr = %q, want mention of unmatched bracket", errOut)
	}
}
