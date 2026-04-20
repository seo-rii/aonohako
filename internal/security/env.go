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

func WorkspaceScopedDirs(workDir string) []string {
	return []string{
		filepath.Join(workDir, ".home"),
		filepath.Join(workDir, ".tmp"),
		filepath.Join(workDir, ".cache"),
		filepath.Join(workDir, ".mpl"),
		filepath.Join(workDir, ".pip-cache"),
		filepath.Join(workDir, ".dotnet-home"),
		filepath.Join(workDir, ".nuget"),
		filepath.Join(workDir, ".konan-home"),
		filepath.Join(workDir, ".konan"),
		filepath.Join(workDir, ".mix"),
		filepath.Join(workDir, ".hex"),
		filepath.Join(workDir, "__img__"),
	}
}

func WorkspaceScopedEnv(workDir string) []string {
	home := filepath.Join(workDir, ".home")
	tmp := filepath.Join(workDir, ".tmp")
	cache := filepath.Join(workDir, ".cache")
	mpl := filepath.Join(workDir, ".mpl")
	pip := filepath.Join(workDir, ".pip-cache")
	dotnetHome := filepath.Join(workDir, ".dotnet-home")
	nuget := filepath.Join(workDir, ".nuget")
	konanHome := filepath.Join(workDir, ".konan-home")
	konan := filepath.Join(workDir, ".konan")
	mix := filepath.Join(workDir, ".mix")
	hex := filepath.Join(workDir, ".hex")
	img := filepath.Join(workDir, "__img__")
	return []string{
		fmt.Sprintf("HOME=%s", home),
		fmt.Sprintf("TMPDIR=%s", tmp),
		fmt.Sprintf("TMP=%s", tmp),
		fmt.Sprintf("TEMP=%s", tmp),
		fmt.Sprintf("TEMPDIR=%s", tmp),
		fmt.Sprintf("JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=%s", tmp),
		fmt.Sprintf("XDG_CACHE_HOME=%s", cache),
		fmt.Sprintf("MPLCONFIGDIR=%s", mpl),
		fmt.Sprintf("PIP_CACHE_DIR=%s", pip),
		fmt.Sprintf("DOTNET_CLI_HOME=%s", dotnetHome),
		fmt.Sprintf("NUGET_PACKAGES=%s", nuget),
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"DOTNET_NOLOGO=1",
		"MSBuildEnableWorkloadResolver=false",
		fmt.Sprintf("KONAN_USER_HOME=%s", konanHome),
		fmt.Sprintf("KONAN_DATA_DIR=%s", konan),
		fmt.Sprintf("MIX_HOME=%s", mix),
		fmt.Sprintf("HEX_HOME=%s", hex),
		fmt.Sprintf("IMG_OUT_DIR=%s", img),
	}
}
