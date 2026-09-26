package main

import (
	"testing"

	"aonohako/internal/runtimepacks"
)

func TestImageFingerprintUsesConditionalModuleInputs(t *testing.T) {
	base := sourceFingerprint{Common: "common", GoModules: "go-a", RustCrates: "rust-a"}
	goChanged := sourceFingerprint{Common: "common", GoModules: "go-b", RustCrates: "rust-a"}
	rustChanged := sourceFingerprint{Common: "common", GoModules: "go-a", RustCrates: "rust-b"}

	plain := runtimepacks.ImageSpec{Name: "type-a", Languages: []string{"plain"}}
	goSpec := runtimepacks.ImageSpec{Name: "type-b", Languages: []string{"go"}}
	rustSpec := runtimepacks.ImageSpec{Name: "type-c", Languages: []string{"rust"}}

	if imageFingerprint(plain, base) != imageFingerprint(plain, goChanged) {
		t.Fatal("non-Go profile changed when only go-modules changed")
	}
	if imageFingerprint(goSpec, base) == imageFingerprint(goSpec, goChanged) {
		t.Fatal("Go profile did not change when go-modules changed")
	}
	if imageFingerprint(plain, base) != imageFingerprint(plain, rustChanged) {
		t.Fatal("non-Rust profile changed when only rust-crates changed")
	}
	if imageFingerprint(rustSpec, base) == imageFingerprint(rustSpec, rustChanged) {
		t.Fatal("Rust profile did not change when rust-crates changed")
	}
}

func TestImageFingerprintUsesResolvedSpec(t *testing.T) {
	source := sourceFingerprint{Common: "common"}
	before := runtimepacks.ImageSpec{Name: "type-a", Languages: []string{"plain"}, AptPackages: []string{"gcc"}}
	after := runtimepacks.ImageSpec{Name: "type-a", Languages: []string{"plain"}, AptPackages: []string{"gcc", "make"}}
	if imageFingerprint(before, source) == imageFingerprint(after, source) {
		t.Fatal("resolved runtime spec change did not affect fingerprint")
	}
}

func TestFingerprintBucketAvoidsCIFeedbackLoop(t *testing.T) {
	for _, path := range []string{
		".github/workflows/ci.yml",
		"docs/architecture.md",
		"internal/runtimepacks/catalog_test.go",
		"cmd/runtime-impact/main.go",
		"cmd/runtime-matrix/main.go",
		"README.md",
	} {
		if got := fingerprintBucket(path); got != "" {
			t.Fatalf("fingerprintBucket(%q) = %q, want ignored", path, got)
		}
	}
	if got := fingerprintBucket("internal/execute/run.go"); got != "common" {
		t.Fatalf("production Go source bucket = %q, want common", got)
	}
	if got := fingerprintBucket("go-modules/go.mod"); got != "go" {
		t.Fatalf("go module bucket = %q, want go", got)
	}
	if got := fingerprintBucket("rust-crates/Cargo.lock"); got != "rust" {
		t.Fatalf("rust crate bucket = %q, want rust", got)
	}
}


func TestParseTreeEntriesPreservesModeAndPath(t *testing.T) {
	entries, err := parseTreeEntries([]byte(
		"100644 blob 0123456789012345678901234567890123456789\tplain.txt\x00" +
			"100755 blob abcdefabcdefabcdefabcdefabcdefabcdefabcd\tscripts/run\twith-tab.sh\x00",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].Mode != "100644" || entries[0].Path != "plain.txt" {
		t.Fatalf("first entry = %#v", entries[0])
	}
	if entries[1].Mode != "100755" || entries[1].Path != "scripts/run\twith-tab.sh" {
		t.Fatalf("second entry = %#v", entries[1])
	}
}
