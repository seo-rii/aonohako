package runtimepacks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type capturedImageEvent struct {
	Mime string `json:"mime"`
	B64  string `json:"b64"`
	TS   int64  `json:"ts"`
}

func requirePython3(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	return python
}

func requirePythonModule(t *testing.T, python string, module string) {
	t.Helper()
	cmd := exec.Command(python, "-c", "import importlib.util, sys; sys.exit(0 if importlib.util.find_spec(sys.argv[1]) else 1)", module)
	if err := cmd.Run(); err != nil {
		t.Skipf("python module %s not available", module)
	}
}

func runSitecustomizeCapture(t *testing.T, script string, files map[string]string) []capturedImageEvent {
	t.Helper()
	python := requirePython3(t)
	root := t.TempDir()
	imgDir := filepath.Join(root, "__img__")
	mplDir := filepath.Join(root, ".mpl")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	if err := os.MkdirAll(mplDir, 0o755); err != nil {
		t.Fatalf("mkdir matplotlib dir: %v", err)
	}

	sitecustomizePath := filepath.Join("..", "..", "python", "sitecustomize.py")
	sitecustomize, err := os.ReadFile(sitecustomizePath)
	if err != nil {
		t.Fatalf("read sitecustomize: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sitecustomize.py"), sitecustomize, 0o644); err != nil {
		t.Fatalf("write sitecustomize fixture: %v", err)
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir fixture %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	cmd := exec.Command(python, "-c", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+root,
		"IMG_CAPTURE=1",
		"IMG_OUT_DIR="+imgDir,
		"MPLCONFIGDIR="+mplDir,
		"PYTHONDONTWRITEBYTECODE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python capture run failed: %v\n%s", err, string(out))
	}

	data, err := os.ReadFile(filepath.Join(imgDir, "images.jsonl"))
	if err != nil {
		t.Fatalf("read image log: %v", err)
	}
	var events []capturedImageEvent
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event capturedImageEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode image event %q: %v", line, err)
		}
		if !strings.HasPrefix(event.Mime, "image/") || event.B64 == "" || event.TS <= 0 {
			t.Fatalf("invalid image event: %+v", event)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatalf("expected at least one image event")
	}
	return events
}

func TestPythonSitecustomizeCapturesMatplotlibShow(t *testing.T) {
	python := requirePython3(t)
	requirePythonModule(t, python, "matplotlib")

	runSitecustomizeCapture(t, `
import matplotlib.pyplot as plt

plt.plot([1, 2, 3], [1, 4, 9])
plt.title("growth")
plt.show()
`, nil)
}

func TestPythonSitecustomizeCapturesSeabornShow(t *testing.T) {
	python := requirePython3(t)
	requirePythonModule(t, python, "matplotlib")
	requirePythonModule(t, python, "seaborn")

	runSitecustomizeCapture(t, `
import matplotlib.pyplot as plt
import seaborn as sns

sns.lineplot(x=[1, 2, 3], y=[2, 3, 5])
plt.show()
`, nil)
}

func TestPythonSitecustomizeCapturesQiskitDrawResults(t *testing.T) {
	events := runSitecustomizeCapture(t, `
from qiskit import QuantumCircuit
from qiskit.visualization import circuit_drawer

qc = QuantumCircuit()
qc.draw(output="mpl")
circuit_drawer(qc, output="mpl")
`, map[string]string{
		"qiskit/__init__.py": `from .circuit import QuantumCircuit
`,
		"qiskit/circuit.py": `import base64

_PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")

class FakeFigure:
    def savefig(self, target, *args, **kwargs):
        target.write(_PNG)

class QuantumCircuit:
    def draw(self, *args, **kwargs):
        return FakeFigure()
`,
		"qiskit/visualization/__init__.py": `from qiskit.circuit import FakeFigure

def circuit_drawer(*args, **kwargs):
    return FakeFigure()
`,
	})

	if len(events) != 2 {
		t.Fatalf("captured %d qiskit draw events, want 2", len(events))
	}
}
