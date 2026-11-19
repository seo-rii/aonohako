package reference

import (
	"embed"
	"fmt"

	"aonohako/internal/profiles"
)

// These are complete independent programs, not one training program dispatched
// into a different branch for validation. CPython/PyPy share source semantics,
// but their compiled artifacts, runtime IDs and observations remain distinct.
//
//go:embed fixtures/runtime/*.py fixtures/runtime/*.rs
var runtimeSources embed.FS

type CorpusProfile struct {
	CorpusID        string
	SourceLanguage  string
	Family          string
	SourceName      string
	ArtifactName    string
	Compiler        string
	CompilerFlags   string
	DefaultMemoryMB int
}

// ResolveCorpusProfile admits only languages with a complete fixed corpus.
// The empty selection preserves the original CPP20 collector API. Registration
// here enables collection, never a time-model profile or a claim of calibration.
func ResolveCorpusProfile(language string) (CorpusProfile, error) {
	if language == "" {
		language = "CPP20"
	}
	profile, ok := profiles.Resolve(language)
	if !ok {
		return CorpusProfile{}, fmt.Errorf("reference collection requires a supported source-language profile")
	}
	result := CorpusProfile{CorpusID: RuntimeCorpusID, SourceLanguage: profile.SourceLang, DefaultMemoryMB: 512}
	switch profile.SourceLang {
	case "CPP20":
		result.CorpusID, result.Family, result.DefaultMemoryMB = CorpusID, "cpp", 256
		result.SourceName, result.ArtifactName, result.Compiler = "Main.cpp", "Main", "g++"
		result.CompilerFlags = collectionCompilerFlags
	case "PYTHON3", "PYPY3":
		result.Family, result.Compiler = "cpython", "python3"
		if profile.SourceLang == "PYPY3" {
			result.Family, result.Compiler = "pypy", "pypy3"
		}
		result.SourceName, result.ArtifactName = "Main.py", "Main.pyc"
		result.CompilerFlags = "-I -S -m compileall -q -b --invalidation-mode checked-hash Main.py"
	case "RUST2024":
		result.Family, result.Compiler = "native", "rustc"
		result.SourceName, result.ArtifactName = "Main.rs", "Main"
		result.CompilerFlags = "--edition 2024 -O --cfg ONLINE_JUDGE"
	default:
		return CorpusProfile{}, fmt.Errorf("reference corpus is not implemented for %s", profile.SourceLang)
	}
	return result, nil
}

func referenceDatasetProfile(dataset Dataset) (CorpusProfile, error) {
	profile, err := ResolveCorpusProfile(dataset.SourceLanguage)
	if err != nil {
		return CorpusProfile{}, err
	}
	if dataset.SourceLanguage != profile.SourceLanguage || dataset.CorpusID != profile.CorpusID || dataset.CorpusSHA256 != "" || dataset.FrozenPolicySHA256 != "" {
		return CorpusProfile{}, fmt.Errorf("reference corpus identity does not match its canonical source-language profile")
	}
	return profile, nil
}

func CorpusForLanguage(language string, smoke bool) ([]Workload, error) {
	profile, err := ResolveCorpusProfile(language)
	if err != nil {
		return nil, err
	}
	if profile.CorpusID == CorpusID {
		return Corpus(smoke), nil
	}
	extension := "py"
	if profile.SourceLanguage == "RUST2024" {
		extension = "rs"
	}
	scales := []int{1, 2, 4}
	iterations, branches, elements := uint64(1<<18), uint64(1<<18), uint64(1<<16)
	bufferBytes, repeats := uint64(256<<10), uint64(128)
	if smoke {
		scales = []int{1}
		iterations, branches, elements, bufferBytes, repeats = 8192, 4096, 1024, 16<<10, 8
	}
	workloads := []Workload{
		{ID: "integer", Split: SplitTrain}, {ID: "copy", Split: SplitTrain},
		{ID: "branch", Split: SplitValidation}, {ID: "sort", Split: SplitValidation},
		{ID: "fill", Split: SplitValidation},
	}
	for i := range workloads {
		data, err := runtimeSources.ReadFile("fixtures/runtime/" + workloads[i].ID + "." + extension)
		if err != nil {
			return nil, fmt.Errorf("reference runtime fixture: %w", err)
		}
		workloads[i].Source = string(data)
	}
	for _, scale := range scales {
		n, branchN, size, repeatN := iterations*uint64(scale), branches*uint64(scale), elements*uint64(scale), repeats*uint64(scale)
		cases := []Case{
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", n, integerSeed), ExpectedStdout: checksumOutput(integerChecksum(n, integerSeed))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", bufferBytes, repeatN), ExpectedStdout: checksumOutput(repCopyChecksum(bufferBytes, repeatN))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", branchN, branchSeed), ExpectedStdout: checksumOutput(branchChecksum(branchN, branchSeed))},
			{Scale: scale, Input: fmt.Sprintf("%d\n", size), ExpectedStdout: checksumOutput(sumSquares(size))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", bufferBytes, repeatN), ExpectedStdout: checksumOutput(repFillChecksum(bufferBytes, repeatN))},
		}
		for i := range workloads {
			workloads[i].Cases = append(workloads[i].Cases, cases[i])
		}
	}
	return workloads, nil
}
