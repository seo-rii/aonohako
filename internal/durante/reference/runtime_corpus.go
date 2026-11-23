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

// SelectCorpusProfile resolves an explicit built-in corpus, or the language's
// original corpus when corpusID is empty.
func SelectCorpusProfile(language, corpusID string) (CorpusProfile, error) {
	profile, err := ResolveCorpusProfile(language)
	if err != nil {
		return CorpusProfile{}, err
	}
	if corpusID == "" || corpusID == profile.CorpusID {
		return profile, nil
	}
	if corpusID != RustLongCorpusID || profile.SourceLanguage != "RUST2024" {
		return CorpusProfile{}, fmt.Errorf("reference corpus %q is not available for %s", corpusID, profile.SourceLanguage)
	}
	profile.CorpusID = RustLongCorpusID
	return profile, nil
}

func referenceDatasetProfile(dataset Dataset) (CorpusProfile, error) {
	profile, err := SelectCorpusProfile(dataset.SourceLanguage, dataset.CorpusID)
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
	return profile.workloads(smoke)
}

func (profile CorpusProfile) workloads(smoke bool) ([]Workload, error) {
	if profile.CorpusID == CorpusID {
		return Corpus(smoke), nil
	}
	extension := "py"
	if profile.SourceLanguage == "RUST2024" {
		extension = "rs"
	}
	scales := []int{1, 2, 4}
	iterations, branches, elements := uint64(1<<18), uint64(1<<18), uint64(1<<16)
	bufferBytes, copyRepeats, fillRepeats := uint64(256<<10), uint64(128), uint64(128)
	if smoke {
		scales = []int{1}
		iterations, branches, elements, bufferBytes, copyRepeats, fillRepeats = 8192, 4096, 1024, 16<<10, 8, 8
	} else if profile.CorpusID == RustLongCorpusID {
		// Keep the buffers and programs fixed while lengthening each optimized
		// Rust workload to exceed the reference fitter's native duration floor.
		iterations, branches, elements = 1<<28, 1<<26, 1<<21
		copyRepeats, fillRepeats = 8192, 16384
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
		multiplier := uint64(scale)
		n, branchN, size := iterations*multiplier, branches*multiplier, elements*multiplier
		copyN, fillN := copyRepeats*multiplier, fillRepeats*multiplier
		cases := []Case{
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", n, integerSeed), ExpectedStdout: checksumOutput(integerChecksum(n, integerSeed))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", bufferBytes, copyN), ExpectedStdout: checksumOutput(repCopyChecksum(bufferBytes, copyN))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", branchN, branchSeed), ExpectedStdout: checksumOutput(branchChecksum(branchN, branchSeed))},
			{Scale: scale, Input: fmt.Sprintf("%d\n", size), ExpectedStdout: checksumOutput(sumSquares(size))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", bufferBytes, fillN), ExpectedStdout: checksumOutput(repFillChecksum(bufferBytes, fillN))},
		}
		for i := range workloads {
			workloads[i].Cases = append(workloads[i].Cases, cases[i])
		}
	}
	return workloads, nil
}
