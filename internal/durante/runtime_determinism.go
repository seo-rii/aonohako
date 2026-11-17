package durante

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// RuntimeDeterminismV1 pins runtime-internal nondeterminism of measured
// executions, which otherwise moves counts between identical runs:
//
//   - Node (run language "javascript"): V8 --predictable, which disables
//     concurrent compilation, background GC and other background tasks.
//   - CPython (run language "python"): hash seed 0. Isolated mode (-I) ignores
//     PYTHONHASHSEED, so -I is replaced by its other two parts, -s and -P, and
//     every PYTHON* variable except PYTHONHASHSEED=0 is removed, which keeps
//     the environment-ignoring effect of -E for everything else.
//
// Program-visible randomness (Math.random, random, os.urandom) is not pinned,
// and other launchers are unchanged: PyPy does not randomize hashes and its
// JIT and GC run in the main thread, and native targets have no runtime
// scheduling of their own. The options change the amount of work, so a time
// model applies only to observations made with the same option set.
const RuntimeDeterminismV1 = "v1"

// ParseRuntimeDeterminism accepts "", "off" (no options) or a known option set.
func ParseRuntimeDeterminism(raw string) (string, error) {
	switch value := strings.TrimSpace(raw); value {
	case "", "off":
		return "", nil
	case RuntimeDeterminismV1:
		return value, nil
	default:
		return "", fmt.Errorf("unknown durante runtime determinism %q", value)
	}
}

func determinismLauncher(runLang string) bool {
	return runLang == "python" || runLang == "javascript"
}

// RuntimeDeterminismFor names the option set a measured execution of the
// normalized run language receives under this configuration. Empty means none.
func (c Config) RuntimeDeterminismFor(runLang string) string {
	if c.RuntimeDeterminism == "" || !determinismLauncher(runLang) {
		return ""
	}
	return c.RuntimeDeterminism
}

// MeasuredRuntime returns the argv and environment of a measured execution:
// the dispatch environment plus the configured runtime determinism options.
// The executor and the effective runtime-options descriptor both use it, so a
// qualified descriptor names exactly what runs. Inputs are not modified. An
// unexpected launcher shape fails closed instead of running unpinned.
func (c Config) MeasuredRuntime(runLang string, argv, env []string) ([]string, []string, error) {
	argv = slices.Clone(argv)
	env = c.RuntimeEnvironment(env)
	switch c.RuntimeDeterminismFor(runLang) {
	case "":
		return argv, env, nil
	case RuntimeDeterminismV1:
	default:
		return nil, nil, fmt.Errorf("unknown durante runtime determinism %q", c.RuntimeDeterminism)
	}
	switch runLang {
	case "javascript":
		if len(argv) < 2 || filepath.Base(argv[0]) != "node" || slices.Contains(argv, "--predictable") {
			return nil, nil, fmt.Errorf("durante runtime determinism does not recognize the node launcher")
		}
		return slices.Insert(argv, 1, "--predictable"), env, nil
	case "python":
		if len(argv) < 2 || filepath.Base(argv[0]) != "python3" || argv[1] != "-I" {
			return nil, nil, fmt.Errorf("durante runtime determinism does not recognize the python launcher")
		}
		argv = slices.Replace(argv, 1, 2, "-s", "-P")
		pinned := make([]string, 0, len(env)+1)
		for _, entry := range env {
			if !strings.HasPrefix(entry, "PYTHON") {
				pinned = append(pinned, entry)
			}
		}
		return argv, append(pinned, "PYTHONHASHSEED=0"), nil
	}
	return nil, nil, fmt.Errorf("durante runtime determinism has no options for %q", runLang)
}
