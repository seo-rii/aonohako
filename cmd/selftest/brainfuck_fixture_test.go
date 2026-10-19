package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrainfuckABProgramComputesDecimalCarriesAndZero(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	program := brainfuckABProgram()
	for _, opcode := range program {
		if !strings.ContainsRune("><+-.,[]", opcode) {
			t.Fatalf("generated source contains a non-Brainfuck character %q", opcode)
		}
	}
	programPath := filepath.Join(t.TempDir(), "Main.bf")
	if err := os.WriteFile(programPath, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, operands := range [][2]int{
		{20, 22}, {10, 13}, {0, 0}, {0, 17}, {91, 8}, {9, 0},
		{12, 88}, {99, 99}, {0, 9}, {9, 9}, {10, 0}, {49, 50},
		{50, 50}, {99, 1}, {99, 0}, {99, 98},
	} {
		a, b := operands[0], operands[1]
		t.Run(fmt.Sprintf("%02d_plus_%02d", a, b), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, pythonPath, filepath.Join("..", "..", "scripts", "brainfuck.py"), programPath)
			cmd.Stdin = strings.NewReader(fmt.Sprintf("%02d %02d\n", a, b))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Brainfuck execution failed: %v: %s", err, output)
			}
			if want := fmt.Sprintf("%d\n", a+b); string(output) != want {
				t.Fatalf("%d + %d = %q, want %q", a, b, output, want)
			}
		})
	}
}
