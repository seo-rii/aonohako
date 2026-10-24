package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSedABProgramComputesUnseenDecimalSums(t *testing.T) {
	cases := []struct {
		stdin string
		want  string
	}{
		{"20 22", "42"},
		{"7 13", "20"},
		{"0 17", "17"},
		{"91 8", "99"},
		{"9 0", "9"},
		{"12 88", "100"},
		{"99 99", "198"},
		{"0 0", "0"},
		{"9 9", "18"},
		{"999 1", "1000"},
		{"1000 2345", "3345"},
		{"12345 67890", "80235"},
		{"00012 00088", "100"},
		{" \t91\t8 \t", "99"},
		{"12345678901234567890 98765432109876543210", "111111111011111111100"},
	}
	var stdin, want strings.Builder
	for _, tc := range cases {
		fmt.Fprintln(&stdin, tc.stdin)
		fmt.Fprintln(&want, tc.want)
	}
	if got := runSedABProgram(t, stdin.String()); got != want.String() {
		t.Fatalf("decimal sums = %q, want %q", got, want.String())
	}
}

func TestSedABProgramComputesAllTwoDigitOperandPairs(t *testing.T) {
	var stdin, want strings.Builder
	for a := 0; a < 100; a++ {
		for b := 0; b < 100; b++ {
			fmt.Fprintf(&stdin, "%d %d\n", a, b)
			fmt.Fprintf(&want, "%d\n", a+b)
		}
	}
	gotLines := strings.Split(strings.TrimSuffix(runSedABProgram(t, stdin.String()), "\n"), "\n")
	wantLines := strings.Split(strings.TrimSuffix(want.String(), "\n"), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("decimal sum count = %d, want %d", len(gotLines), len(wantLines))
	}
	for index, got := range gotLines {
		if got != wantLines[index] {
			t.Fatalf("%d + %d = %q, want %q", index/100, index%100, got, wantLines[index])
		}
	}
}

func runSedABProgram(t *testing.T, stdin string) string {
	t.Helper()
	sedPath, err := exec.LookPath("sed")
	if err != nil {
		t.Skip("sed is not installed")
	}
	scriptPath := filepath.Join(t.TempDir(), "Main.sed")
	if err := os.WriteFile(scriptPath, []byte(sedABProgram), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sedPath, "-f", scriptPath)
	cmd.Stdin = strings.NewReader(stdin)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sed execution failed: %v: %s", err, output)
	}
	return string(output)
}
