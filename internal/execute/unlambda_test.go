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

// unlambdaCat reads bytes with @ and echoes each one with | until EOF, using
// self-application (```sii M) so the loop runs in constant continuation space.
const unlambdaCat = "```sii``s`k@``s`k`s``si`k|``s`kk``s``s`ks``s`k`sikk"

func runUnlambda(t *testing.T, program, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	src := filepath.Join(t.TempDir(), "Main.unl")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write program: %v", err)
	}
	cmd := exec.Command(python, filepath.Join("..", "..", "scripts", "unlambda.py"), src)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run unlambda: %v", runErr)
		}
	}
	return out.String(), errBuf.String(), code
}

func TestBundledUnlambdaInterpreterRunsHelloWorld(t *testing.T) {
	out, errOut, code := runUnlambda(t, "`r```````````.H.e.l.l.o. .w.o.r.l.di\n", "")
	if code != 0 || out != "Hello world\n" {
		t.Fatalf("hello world: out=%q exit=%d stderr=%q", out, code, errOut)
	}
}

func TestBundledUnlambdaInterpreterEchoesInputUntilEOF(t *testing.T) {
	for _, stdin := range []string{"ab\nc", "", "\x00\xff \n"} {
		out, errOut, code := runUnlambda(t, unlambdaCat, stdin)
		if code != 0 || out != stdin {
			t.Fatalf("cat(%q): out=%q exit=%d stderr=%q", stdin, out, code, errOut)
		}
	}
}

func TestBundledUnlambdaInterpreterFollowsReferenceSemantics(t *testing.T) {
	cases := []struct {
		name, program, stdin, want string
	}{
		{"d delays its operand", "`.a`d`.bi", "", "a"},
		{"forced promise evaluates once applied", "``d`.bi.c", "", "b"},
		{"promise forced on each application", "```s`kd`.ai.b", "", "a"},
		{"d as s operand delays the second branch", "```sd`.ai.b", "", "ab"},
		{"k evaluates both operands", "```kd`.ai.b", "", "a"},
		{"continuation re-enters application", "``.a`ci.b", "", "aab"},
		{"continuation re-evaluates operand", "`.x``ci`.yi", "", "yyx"},
		{"call/cc of call/cc", "``cc``cc.x", "", "xxx"},
		{"cir prints one newline", "``cir", "", "\n"},
		{"e exits before later output", "```.1e.2.3", "", "1"},
		{"e exits from an operand", "`.1``.2e.3", "", "2"},
		{"@ applies operand to i on success", "```@.Y.N.Z", "q", "YN"},
		{"@ applies operand to v at EOF", "```@.Y.N.Z", "", "Y"},
		{"?x matches current char", "``@i```?a.y.n.z", "a", "yn"},
		{"?x rejects other char", "``@i```?a.y.n.z", "b", "y"},
		{"?x has no current char at EOF", "``@i```?a.y.n.z", "", "y"},
		{"| reprints current char", "``@i``|.ii", "q", "iq"},
		{"| yields v at EOF", "``@i```|.i.j.k", "", "i"},
		{"literal # after .", "``.#`d.a.b", "", "#a"},
		{"literal newline after .", "``.\ni.c", "", "\n"},
		{"literal space after .", "`. i", "", " "},
		{"literal newline after ?", "``@i```?\n.y.n.z", "\n", "yn"},
		{"upper-case builtins and comments", "# greet\n`R ``K`.!II # trailing comment\n", "", "!\n"},
	}
	for _, tc := range cases {
		out, errOut, code := runUnlambda(t, tc.program, tc.stdin)
		if code != 0 || out != tc.want {
			t.Errorf("%s: %q stdin=%q: out=%q exit=%d stderr=%q, want %q", tc.name, tc.program, tc.stdin, out, code, errOut, tc.want)
		}
	}
}

func TestBundledUnlambdaInterpreterHandlesDeepPrograms(t *testing.T) {
	const succ = "`s``s`ksk"
	two := "`" + succ + "i"
	five := "`" + succ + "`" + succ + "`" + succ + two
	ten := "``s`k" + two + five
	// (10^5 .x) i prints 100000 x, then e exits before `.!i is evaluated.
	loop := "``e```" + five + ten + ".xi`.!i"
	leftNested := strings.Repeat("`", 100000) + strings.Repeat(".a", 100000) + "i"
	rightNested := strings.Repeat("`.b", 100000) + "i"
	for name, tc := range map[string]struct{ program, want string }{
		"church loop":  {loop, strings.Repeat("x", 100000)},
		"left nested":  {leftNested, strings.Repeat("a", 100000)},
		"right nested": {rightNested, strings.Repeat("b", 100000)},
	} {
		out, errOut, code := runUnlambda(t, tc.program, "")
		if code != 0 || out != tc.want {
			t.Fatalf("%s: len(out)=%d exit=%d stderr=%q", name, len(out), code, errOut)
		}
	}
}

func TestBundledUnlambdaInterpreterRejectsMalformedSource(t *testing.T) {
	for _, program := range []string{"", "`i", "`ii i", "`.", "`ia", "# only a comment\n"} {
		out, errOut, code := runUnlambda(t, program, "")
		if code == 0 || out != "" || !strings.HasPrefix(errOut, "unlambda: ") {
			t.Fatalf("malformed %q: out=%q exit=%d stderr=%q", program, out, code, errOut)
		}
	}
}

func TestBundledUnlambdaInterpreterExposesParseAndRun(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	helperPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "unlambda.py"))
	if err != nil {
		t.Fatalf("Abs(helper): %v", err)
	}
	probe := `import importlib.util
import io
import os

spec = importlib.util.spec_from_file_location("aonohako_unlambda", os.environ["UNLAMBDA_HELPER"])
unlambda = importlib.util.module_from_spec(spec)
spec.loader.exec_module(unlambda)

def run(source, stdin=b""):
    stdout = io.BytesIO()
    unlambda.run(unlambda.parse(source), io.BytesIO(stdin), stdout)
    return stdout.getvalue()

# Go raw strings cannot hold backticks, so the application operator is \x60.
assert run(b"\x60 .\xff i") == b"\xff"
assert run(b"\x60\x60@i\x60|i", b"\x80") == b""
assert run(b"\x60\x60@i\x60\x60|.ii", b"\x80") == b"i\x80"
for bad in (b"", b"\x60\x60ii", b"ii", b"?"):
    try:
        unlambda.parse(bad)
    except unlambda.UnlambdaError:
        pass
    else:
        raise AssertionError(bad)
`
	cmd := exec.Command(python, "-c", probe)
	cmd.Env = append(os.Environ(), "UNLAMBDA_HELPER="+helperPath, "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Unlambda module probe failed: %v\n%s", err, output)
	}
}
