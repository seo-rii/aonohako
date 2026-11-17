package reference

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
)

func TestRuntimeCorpusPreservesCPPAndCanonicalSelection(t *testing.T) {
	for _, language := range []string{"", "CPP20", " cpp20 "} {
		profile, err := ResolveCorpusProfile(language)
		if err != nil || profile.CorpusID != CorpusID || profile.SourceLanguage != "CPP20" || profile.DefaultMemoryMB != 256 {
			t.Fatalf("legacy selection changed: %+v %v", profile, err)
		}
		for _, smoke := range []bool{false, true} {
			got, err := CorpusForLanguage(language, smoke)
			if err != nil || !reflect.DeepEqual(got, Corpus(smoke)) {
				t.Fatal("existing CPP20 sources, inputs or splits changed", err)
			}
		}
	}
	for _, language := range []string{"binary", "unknown", "C11", "JAVA21", "RUST2021"} {
		if _, err := ResolveCorpusProfile(language); err == nil {
			t.Fatal("source without fixed corpus was implicitly admitted", language)
		}
	}
}

func TestRuntimeCorpusLanguagesShareInputsOracleAndHeldOutSplits(t *testing.T) {
	for _, smoke := range []bool{false, true} {
		baseline, _ := CorpusForLanguage("PYTHON3", smoke)
		for _, language := range []string{"PYTHON3", "PYPY3", "RUST2024"} {
			profile, err := ResolveCorpusProfile(language)
			if err != nil || profile.CorpusID != RuntimeCorpusID || profile.DefaultMemoryMB != 512 {
				t.Fatalf("runtime profile: %+v %v", profile, err)
			}
			corpus, err := CorpusForLanguage(language, smoke)
			if err != nil || len(corpus) != 5 {
				t.Fatal("missing runtime programs", err)
			}
			seen := map[string]bool{}
			for i, workload := range corpus {
				if workload.ID != baseline[i].ID || workload.Split != baseline[i].Split || !reflect.DeepEqual(workload.Cases, baseline[i].Cases) {
					t.Fatal("runtime languages do not share fixed task semantics")
				}
				wantSplit := SplitValidation
				if workload.ID == "integer" || workload.ID == "copy" {
					wantSplit = SplitTrain
				}
				if workload.Split != wantSplit || seen[workload.Source] || workload.Source == "" || !strings.HasSuffix(workload.Source, "\n") {
					t.Fatal("program reused or train/held-out boundary changed", workload.ID)
				}
				seen[workload.Source] = true
			}
			corpus[0].Cases[0].Input = "mutated"
			fresh, _ := CorpusForLanguage(language, smoke)
			if fresh[0].Cases[0].Input == "mutated" {
				t.Fatal("corpus call aliases mutable fixtures")
			}
		}
	}
}

// These are new synthetic observations with the same known arithmetic as the
// legacy fixture, not relabeled historical collection records or calibration.
func syntheticRuntimeDataset(language string) Dataset {
	dataset := syntheticDataset()
	profile, err := ResolveCorpusProfile(language)
	if err != nil {
		panic(err)
	}
	dataset.CorpusID, dataset.SourceLanguage = profile.CorpusID, profile.SourceLanguage
	corpus, err := CorpusForLanguage(language, false)
	if err != nil {
		panic(err)
	}
	index := 0
	for _, workload := range corpus {
		for _, fixture := range workload.Cases {
			for repetition := 1; repetition <= dataset.Repetitions; repetition++ {
				sample := &dataset.Samples[index]
				index++
				sample.WorkloadID, sample.Split = workload.ID, workload.Split
				sample.SourceSHA256, sample.InputSHA256, sample.ExpectedSHA256 = referenceDigest(workload.Source), referenceDigest(fixture.Input), referenceDigest(fixture.ExpectedStdout)
				sample.BinarySHA256 = referenceDigest("synthetic " + language + " artifact " + workload.ID)
				sample.Metered.WorkMeasurement.Family = profile.Family
			}
		}
	}
	return dataset
}

func TestRuntimeFitBindsOnlyTheObservedLanguage(t *testing.T) {
	for _, language := range []string{"PYTHON3", "PYPY3", "RUST2024"} {
		t.Run(language, func(t *testing.T) {
			dataset := syntheticRuntimeDataset(language)
			before, _ := json.Marshal(dataset)
			result, err := Fit(dataset, syntheticFitOptions())
			if err != nil || !result.Passed || result.CandidatePolicy == nil {
				t.Fatalf("synthetic runtime fit: %+v %v", result, err)
			}
			policy := result.CandidatePolicy
			family, _ := durante.FamilyForLanguage(language)
			if policy.RuntimeID != language+"@"+dataset.RuntimeImage || policy.Family != family || policy.InstructionNSNumerator != 2*coefficientDenominator || policy.REPByteNSNumerator != 3*coefficientDenominator {
				t.Fatalf("runtime fit lost identity or exact common arithmetic: %+v", policy)
			}
			after, _ := json.Marshal(dataset)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("fitting changed observations or implied another runtime")
			}
		})
	}
}

func TestRuntimeFitRejectsCorpusRelabelingAndUnsupportedMethods(t *testing.T) {
	for name, edit := range map[string]func(*Dataset, *FitOptions){
		"legacy corpus identity":       func(d *Dataset, _ *FitOptions) { d.CorpusID = CorpusID },
		"C++ source in runtime corpus": func(d *Dataset, _ *FitOptions) { d.SourceLanguage = "CPP20" },
		"unknown source":               func(d *Dataset, _ *FitOptions) { d.SourceLanguage = "JAVA21" },
		"noncanonical source":          func(d *Dataset, _ *FitOptions) { d.SourceLanguage = "python3" },
		"another family report":        func(d *Dataset, _ *FitOptions) { d.Samples[0].Metered.WorkMeasurement.Family = "cpp" },
		"another fixture":              func(d *Dataset, _ *FitOptions) { d.Samples[0].SourceSHA256 = referenceDigest("other source") },
		"unknown method":               func(_ *Dataset, o *FitOptions) { o.Method = "weighted-rep-operations-nanoseconds-v2" },
	} {
		t.Run(name, func(t *testing.T) {
			dataset, options := syntheticRuntimeDataset("PYTHON3"), syntheticFitOptions()
			edit(&dataset, &options)
			result, err := Fit(dataset, options)
			if err == nil || result.Passed || result.CandidatePolicy != nil {
				t.Fatalf("incompatible runtime data was fitted: %+v %v", result, err)
			}
		})
	}
}

func TestRuntimeFitReportsUnidentifiableREPWeight(t *testing.T) {
	dataset := syntheticRuntimeDataset("PYTHON3")
	for i := range dataset.Samples {
		report := dataset.Samples[i].Metered.WorkMeasurement
		zero := uint64(0)
		report.REPBytes, report.REPEntries = &zero, &zero
	}
	result, err := Fit(dataset, syntheticFitOptions())
	if err != nil || result.Passed || result.CandidatePolicy != nil || !strings.Contains(strings.Join(result.Failures, " "), "rank deficient") {
		t.Fatalf("missing REP variation was hidden: %+v %v", result, err)
	}
}

// A pinned collection fits only pinned observations, and the dataset cannot
// claim options its source launcher does not have.
func TestRuntimeFitRequiresRecordedRuntimeDeterminism(t *testing.T) {
	pinned := func(language, recorded, observed string) Dataset {
		dataset := syntheticRuntimeDataset(language)
		dataset.RuntimeDeterminism = recorded
		for i := range dataset.Samples {
			dataset.Samples[i].Metered.WorkMeasurement.RuntimeDeterminism = observed
		}
		return dataset
	}
	v1 := durante.RuntimeDeterminismV1
	for _, language := range []string{"PYTHON3", "PYPY3"} {
		if result, err := Fit(pinned(language, v1, v1), syntheticFitOptions()); err != nil || !result.Passed || result.CandidatePolicy == nil {
			t.Fatalf("pinned %s fit: %+v %v", language, result, err)
		}
	}
	for _, tc := range []struct{ language, recorded, observed string }{
		{"PYTHON3", v1, ""}, {"PYTHON3", "", v1}, {"PYPY3", v1, ""}, {"RUST2024", v1, v1},
	} {
		if result, err := Fit(pinned(tc.language, tc.recorded, tc.observed), syntheticFitOptions()); err == nil && result.Passed {
			t.Fatalf("%s recorded %q observed %q fitted", tc.language, tc.recorded, tc.observed)
		}
	}
}
