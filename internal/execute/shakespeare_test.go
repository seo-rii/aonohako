package execute

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBundledShakespeareRunnerUsesJudgeNumericInput(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	helperPath, err := filepath.Abs(filepath.Join("..", "..", "scripts", "shakespeare_run.py"))
	if err != nil {
		t.Fatalf("Abs(helper): %v", err)
	}
	probe := `import importlib.util
import io
import os

spec = importlib.util.spec_from_file_location("aonohako_shakespeare", os.environ["SHAKESPEARE_HELPER"])
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)

def manager(data):
    return runner.ByteInputManager(io.BufferedReader(io.BytesIO(data)), lambda: None)

values = manager(b"  20 22\n-4\t+17\n")
assert [values.consume_numeric_input() for _ in range(4)] == [20, 22, -4, 17]

mixed = manager(b"7\nA")
assert mixed.consume_numeric_input() == 7
assert mixed.consume_character_input() == ord("A")
assert mixed.consume_character_input() == -1

for data, message in ((b"", "End of file"), (b" \n", "End of file"), (b"x", "No numeric"), (b"-", "No numeric")):
    try:
        manager(data).consume_numeric_input()
    except runner.SplRunnerError as exc:
        assert message in str(exc), (data, exc)
    else:
        raise AssertionError(data)

out = io.BytesIO()
writer = runner.ByteOutputManager(out)
writer.output_number(-13)
writer.output_character(10)
writer.output_character(0xFF)
writer.output_character(0xAC00)
assert out.getvalue() == b"-13\n\xff" + "가".encode("utf-8")
try:
    writer.output_character(-1)
except runner.SplRunnerError:
    pass
else:
    raise AssertionError("negative character code accepted")
`
	cmd := exec.Command(python, "-c", probe)
	cmd.Env = append(os.Environ(), "SHAKESPEARE_HELPER="+helperPath, "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Shakespeare runner I/O probe failed: %v\n%s", err, output)
	}
}

func TestBundledShakespeareRunnerRejectsBadUsage(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	cmd := exec.Command(python, filepath.Join("..", "..", "scripts", "shakespeare_run.py"))
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("usage exit = %v, want status 1\n%s", err, output)
	}
}
