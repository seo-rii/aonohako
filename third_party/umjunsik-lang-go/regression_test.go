//go:build uhmlang_regression

package uhmlang_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run as a standalone file so the upstream interpreter and these tests need
// only the Go standard library, not aonohako's server dependencies.
func run(t *testing.T, source, input, want string, exit int) {
	t.Helper()
	binary := os.Getenv("UHMLANG_BINARY")
	if binary == "" {
		t.Fatal("UHMLANG_BINARY must name the compiled interpreter")
	}
	path := filepath.Join(t.TempDir(), "Main.uhm")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, path)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("interpreter timed out; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	gotExit := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			gotExit = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if gotExit != exit || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want exit=%d stdout=%q", gotExit, stdout.String(), stderr.String(), exit, want)
	}
}

func TestControlFlow(t *testing.T) {
	cases := []struct {
		name, source, want string
	}{
		{"forward", "어떻게\n준....\n식.!\n식..!\n이 사람이름이냐ㅋㅋ", "2"},
		{"consecutive-blanks", "어떻게\n준......\n\n\n식.!\n식..!\n이 사람이름이냐ㅋㅋ", "2"},
		{"jump-to-blank", "어떻게\n준.....\n식.!\n식..!\n\n식...!\n이 사람이름이냐ㅋㅋ", "3"},
		{"empty-assignment", "어떻게\n엄\n\n준......\n식.!\n식어!\n이 사람이름이냐ㅋㅋ", "0"},
		{"empty-indexed-assignment", "어떻게\n어엄\n\n준......\n식.!\n식어어!\n이 사람이름이냐ㅋㅋ", "0"},
		{"conditional-taken", "어떻게\n동탄어?준....\n식.!\n식..!\n이 사람이름이냐ㅋㅋ", "2"},
		{"conditional-not-taken", "어떻게\n동탄.?준....\n식.!\n식..!\n이 사람이름이냐ㅋㅋ", "12"},
		{"jump-to-footer", "어떻게\n준....\n식.!\n이 사람이름이냐ㅋㅋ", ""},
		{"variable-target", "어떻게\n엄.....\n준어\n식.!\n식..!\n이 사람이름이냐ㅋㅋ", "2"},
		{"backward", "어떻게\n엄...\n식어!\n엄어,\n동탄어?준.......\n준...\n이 사람이름이냐ㅋㅋ", "321"},
	}
	for _, tc := range cases {
		for _, separator := range []struct{ name, value string }{{"lf", "\n"}, {"crlf", "\r\n"}, {"tilde", "~"}} {
			t.Run(tc.name+"/"+separator.name, func(t *testing.T) {
				run(t, strings.ReplaceAll(tc.source, "\n", separator.value), "", tc.want, 0)
			})
		}
	}
}

func TestIntegerInput(t *testing.T) {
	source := "어떻게\n엄식?\n어엄식?\n식어!\n식ㅋ\n식어어!\n이 사람이름이냐ㅋㅋ"
	for _, tc := range []struct{ name, input, want string }{
		{"spaces", "1 4\n", "1\n4"},
		{"newlines", "1\n4\n", "1\n4"},
		{"tabs", "1\t4", "1\n4"},
		{"mixed-whitespace", " \r\n\t-17 \r\n 23", "-17\n23"},
		{"no-final-newline", "5 1", "5\n1"},
		{"zero", "0 0", "0\n0"},
	} {
		t.Run(tc.name, func(t *testing.T) { run(t, source, tc.input, tc.want, 0) })
	}
}

// Independently written repeated-addition program. Labels are resolved against
// physical source lines, including every inserted blank line and the header.
// It exercises the same control-flow failure as Jungol submission 13681383
// without copying the submitted solution into this repository.
func sumsProgram(padding int, separator string) string {
	instructions := []string{
		"어떻게", "엄식?", "@next:동탄어?준@exit", "어엄식?", "어어엄식?",
		"@add:동탄어어?준@print", "어엄어어,", "어어엄어어어.", "준@add",
		"@print:식어어어!", "식ㅋ", "엄어,", "준@next", "@exit:이 사람이름이냐ㅋㅋ",
	}
	var lines []string
	labels := make(map[string]int)
	for i, instruction := range instructions {
		if i > 0 {
			for j := 0; j < (i*7+padding)%(padding+1); j++ {
				lines = append(lines, "")
			}
		}
		if strings.HasPrefix(instruction, "@") {
			parts := strings.SplitN(instruction, ":", 2)
			labels[parts[0]] = len(lines) + 1
			instruction = parts[1]
		}
		lines = append(lines, instruction)
	}
	for i, line := range lines {
		for label, number := range labels {
			line = strings.ReplaceAll(line, label, strings.Repeat(".", number))
		}
		lines[i] = line
	}
	return strings.Join(lines, separator)
}

func TestRepeatedAddition(t *testing.T) {
	var input, want strings.Builder
	fmt.Fprintln(&input, 100)
	for i := 0; i < 100; i++ {
		a, b := i%17, i*19-500
		fmt.Fprintln(&input, a, b)
		fmt.Fprintln(&want, a+b)
	}
	for padding := 0; padding <= 8; padding++ {
		for _, separator := range []struct{ name, value string }{{"lf", "\n"}, {"crlf", "\r\n"}, {"tilde", "~"}} {
			t.Run(strconv.Itoa(padding)+"/"+separator.name, func(t *testing.T) {
				source := sumsProgram(padding, separator.value)
				run(t, source, "4\n1 4\n2 7\n1 2\n4 4", "5\n9\n3\n8\n", 0)
				run(t, source, "0", "", 0)
				run(t, source, input.String(), want.String(), 0)
			})
		}
	}
}

func TestExitStatus(t *testing.T) {
	run(t, "어떻게\n화이팅!.......\n식.!\n이 사람이름이냐ㅋㅋ", "", "", 7)
}

func TestInfiniteLoopRemainsBoundedByCaller(t *testing.T) {
	binary := os.Getenv("UHMLANG_BINARY")
	if binary == "" {
		t.Fatal("UHMLANG_BINARY must name the compiled interpreter")
	}
	path := filepath.Join(t.TempDir(), "Loop.uhm")
	if err := os.WriteFile(path, []byte("어떻게\n준..\n이 사람이름이냐ㅋㅋ"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := exec.CommandContext(ctx, binary, path).Run()
	if ctx.Err() != context.DeadlineExceeded || err == nil {
		t.Fatalf("loop unexpectedly terminated instead of requiring caller timeout: %v", err)
	}
}
