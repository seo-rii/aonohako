package security

import (
	"fmt"
	"path/filepath"
)

func ThreadLimitEnv() []string {
	return []string{
		"GOMAXPROCS=1",
		"OMP_NUM_THREADS=1",
		"OPENBLAS_NUM_THREADS=1",
		"MKL_NUM_THREADS=1",
		"NUMEXPR_NUM_THREADS=1",
		"VECLIB_MAXIMUM_THREADS=1",
		"BLIS_NUM_THREADS=1",
		"TF_NUM_INTRAOP_THREADS=1",
		"TF_NUM_INTEROP_THREADS=1",
		"XLA_FLAGS=--xla_cpu_multi_thread_eigen=false intra_op_parallelism_threads=1",
	}
}

func WorkspaceScopedEnv(workDir string) []string {
	home := filepath.Join(workDir, ".home")
	tmp := filepath.Join(workDir, ".tmp")
	cache := filepath.Join(workDir, ".cache")
	mpl := filepath.Join(workDir, ".mpl")
	pip := filepath.Join(workDir, ".pip-cache")
	img := filepath.Join(workDir, "__img__")
	return []string{
		fmt.Sprintf("HOME=%s", home),
		fmt.Sprintf("TMPDIR=%s", tmp),
		fmt.Sprintf("XDG_CACHE_HOME=%s", cache),
		fmt.Sprintf("MPLCONFIGDIR=%s", mpl),
		fmt.Sprintf("PIP_CACHE_DIR=%s", pip),
		fmt.Sprintf("IMG_OUT_DIR=%s", img),
	}
}
