package execute

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const malbolgeHelloWorld = "('&%:9]!~}|z2Vxwv-,POqponl$Hjig%eB@@>}=<M:9wv6WsU2T|nm-,jcL(I&%$#\"`CB]V?Tx<uVtT`Rpo3NlF.Jh++FdbCBA@?]!~|4XzyTT43Qsqq(Lnmkj\"Fhg${z@>"

func TestBundledMalbolgeInterpreterRunsReferenceHelloWorld(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	sourcePath := filepath.Join(t.TempDir(), "Main.mal")
	if err := os.WriteFile(sourcePath, []byte(malbolgeHelloWorld+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", sourcePath, err)
	}
	output, err := exec.Command(python, filepath.Join("..", "..", "scripts", "malbolge.py"), sourcePath).Output()
	if err != nil {
		t.Fatalf("malbolge helper failed: %v", err)
	}
	if string(output) != "Hello World!" {
		t.Fatalf("malbolge output = %q, want exact reference output", string(output))
	}
}

func TestBundledMalbolgeInterpreterPreservesReferenceByteAndMutationSemantics(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	helperPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "malbolge.py"))
	if err != nil {
		t.Fatalf("Abs(helper): %v", err)
	}
	probe := `import importlib.util
import io
import os

spec = importlib.util.spec_from_file_location("aonohako_malbolge", os.environ["MALBOLGE_HELPER"])
malbolge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(malbolge)

def encoded(opcode, position):
    return 33 + (malbolge.XLAT1.index(ord(opcode)) - position) % len(malbolge.XLAT1)

def io_program(stdin):
    memory = [33] * malbolge.MEMORY_SIZE
    memory[0] = encoded("/", 0)
    memory[1] = encoded("<", 1)
    memory[2] = encoded("v", 2)
    stdout = io.BytesIO()
    malbolge.execute(memory, io.BytesIO(stdin), stdout)
    return stdout.getvalue()

assert io_program(b"\xff") == b"\xff"
assert io_program(b"") == bytes(((malbolge.MEMORY_SIZE - 1) & 0xff,))
assert malbolge.crazy(0, 0) == 29524

memory = [33] * malbolge.MEMORY_SIZE
destination = encoded("i", 0)
memory[0] = destination
memory[destination] = 33
memory[destination + 1] = encoded("v", destination + 1)
malbolge.execute(memory, io.BytesIO(), io.BytesIO())
assert memory[destination] == malbolge.XLAT2[0]
	`
	cmd := exec.Command(python, "-c", probe)
	cmd.Env = append(os.Environ(), "MALBOLGE_HELPER="+helperPath, "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Malbolge reference semantics probe failed: %v\n%s", err, output)
	}
}
