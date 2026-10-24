package execute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aonohako/internal/model"
)

func runGolfScript(t *testing.T, program, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby not available")
	}
	src := filepath.Join(t.TempDir(), "Main.gs")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := buildCommand(src, "golfscript", &model.RunRequest{})
	// Relocate only the image-owned interpreter; keep the actual runtime flags.
	args[1] = filepath.Join("..", "..", "third_party", "golfscript", "golfscript.rb")
	cmd := exec.CommandContext(ctx, ruby, args[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("GolfScript timed out: %v", ctx.Err())
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run GolfScript: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return out.String(), errOut.String(), code
}

func TestGolfScriptRuntimeCommandDisablesRubyInterpolation(t *testing.T) {
	want := []string{"ruby", "/usr/local/lib/aonohako/golfscript.rb", "-n", "/tmp/Main.gs"}
	if got := buildCommand("/tmp/Main.gs", "golfscript", &model.RunRequest{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("GolfScript command = %q, want %q", got, want)
	}
}

func TestGolfScriptVendoredInterpreterChecksum(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "third_party", "golfscript", "golfscript.rb"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "84f932a624b19afe6ef2a3ebe09a9b832f765a87460a79cde22323615c468f38"
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
		t.Fatalf("vendored interpreter checksum = %s, want %s", got, want)
	}
}

func TestGolfScriptStandardStackOperations(t *testing.T) {
	for _, tc := range []struct {
		name, program, stdin, want string
	}{
		{"input is one string", "", "1 2\n", "1 2\n\n"},
		{"integer literals", ";1 2+", "", "3\n"},
		{"implicit output", `;"ok"`, "", "ok\n"},
		{"explicit output", `;"ok"puts`, "", "ok\n\n"},
		{"array fold", ";[1 2 3]{+}*", "", "6\n"},
		{"block evaluation", ";{1 2+}~", "", "3\n"},
		{"stack copy", ";1 2 1$+", "", "13\n"},
		{"inspect", ";[1 2]`", "", "[1 2]\n"},
		{"assignment", ";7:x;x x+", "", "14\n"},
		{"single quoted string", `;'a\\nb'`, "", "a\\nb\n"},
		{"escaped string", `;"a\nb"`, "", "a\nb\n"},
		{"comment", ";1 # system exec eval File are comment text\n2+", "", "3\n"},
		{"literal restricted words", `;"system exec eval IO File Dir Kernel"`, "", "system exec eval IO File Dir Kernel\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := runGolfScript(t, tc.program, tc.stdin)
			if code != 0 || out != tc.want || stderr != "" {
				t.Fatalf("out=%q exit=%d stderr=%q, want %q", out, code, stderr, tc.want)
			}
		})
	}
}

func TestGolfScriptPlusRequiresTwoOperands(t *testing.T) {
	out, stderr, code := runGolfScript(t, "+", "1 2\n")
	if code == 0 || out != "" || !strings.Contains(stderr, "pop on empty stack") {
		t.Fatalf("out=%q exit=%d stderr=%q, want stack underflow", out, code, stderr)
	}
}

func TestGolfScriptRubyInterpolationCannotExecute(t *testing.T) {
	for _, viaStdin := range []bool{false, true} {
		t.Run(fmt.Sprintf("stdin=%t", viaStdin), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ruby-leak")
			literal := fmt.Sprintf("#{File.write('%s', 'leaked')}", marker)
			program, stdin := `;"`+literal+`"`, ""
			if viaStdin {
				program, stdin = "~", `"`+literal+`"`
			}
			out, stderr, code := runGolfScript(t, program, stdin)
			if code != 0 || out != literal+"\n" || stderr != "" {
				t.Fatalf("out=%q exit=%d stderr=%q, want literal interpolation", out, code, stderr)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Ruby interpolation created a file or stat failed: %v", err)
			}
		})
	}
}

func TestGolfScriptAPlusB(t *testing.T) {
	for _, program := range []string{"~ +", "~+"} {
		for _, tc := range []struct {
			name, stdin, want string
		}{
			{"sample", "1 2\n", "3\n"},
			{"different input", "20 22\n", "42\n"},
			{"negative", "-3 10\n", "7\n"},
			{"zero", "0 0\n", "0\n"},
			{"whitespace", " \t-5\r\n-7\t\n", "-12\n"},
			{"no trailing newline", "7 13", "20\n"},
			{"integer precision", "9007199254740993 1\n", "9007199254740994\n"},
		} {
			t.Run(program+"/"+tc.name, func(t *testing.T) {
				out, stderr, code := runGolfScript(t, program, tc.stdin)
				if code != 0 || out != tc.want || stderr != "" {
					t.Fatalf("out=%q exit=%d stderr=%q, want %q", out, code, stderr, tc.want)
				}
			})
		}
	}
}
