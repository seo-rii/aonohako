package compile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"aonohako/internal/compilewire"
	"aonohako/internal/model"
)

func TestArtifactCollectionEnforcesRemoteMetadataBounds(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= compilewire.ArtifactCount; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := collectArtifacts(root, nil, ""); err == nil || !strings.Contains(err.Error(), "too many artifacts") {
		t.Fatalf("artifact count rejection: %v", err)
	}
	prefix := strings.Repeat("a", compilewire.ArtifactNameBytes)
	if _, err := collectArtifacts(root, nil, prefix); err == nil || !strings.Contains(err.Error(), "name too long") {
		t.Fatalf("artifact path rejection: %v", err)
	}
	if _, err := readSingleArtifact(root, "0000", prefix+"a", ""); err == nil || !strings.Contains(err.Error(), "name too long") {
		t.Fatalf("single artifact path rejection: %v", err)
	}
}

func TestPrepareCompileResponseBoundsErrorDiagnostics(t *testing.T) {
	original := model.CompileResponse{
		Status: model.CompileStatusInvalid,
		Stdout: strings.Repeat("<", compilewire.OutputBytes+1),
		Stderr: strings.Repeat("\x00", compilewire.OutputBytes+1),
		Reason: strings.Repeat("\xff", compilewire.ReasonBytes+1),
	}
	got := PrepareResponse(original)
	if len(got.Stdout) != compilewire.OutputBytes || len(got.Stderr) != compilewire.OutputBytes || len(got.Reason) != compilewire.ReasonBytes || !got.StdoutTruncated || !got.StderrTruncated || !got.ReasonTruncated {
		t.Fatalf("diagnostic lengths/flags: %d/%d/%d %v/%v/%v", len(got.Stdout), len(got.Stderr), len(got.Reason), got.StdoutTruncated, got.StderrTruncated, got.ReasonTruncated)
	}
	if original.ReasonTruncated || len(original.Reason) != compilewire.ReasonBytes+1 {
		t.Fatal("response preparation mutated the original")
	}
}

func TestPrepareCompileResponsePreservesReasonUTF8AndRejectsUnboundedCode(t *testing.T) {
	reason := strings.Repeat("한글🙂", compilewire.ReasonBytes/10+1)
	got := PrepareResponse(model.CompileResponse{Status: model.CompileStatusInvalid, Reason: reason})
	if len(got.Reason) > compilewire.ReasonBytes || !utf8.ValidString(got.Reason) || !strings.HasPrefix(reason, got.Reason) || !got.ReasonTruncated {
		t.Fatalf("reason prefix: length=%d valid=%v truncated=%v", len(got.Reason), utf8.ValidString(got.Reason), got.ReasonTruncated)
	}
	got = PrepareResponse(model.CompileResponse{Status: model.CompileStatusOK, ReasonCode: strings.Repeat("a", compilewire.ReasonCodeBytes+1)})
	if got.Status != model.CompileStatusInternal || !strings.Contains(got.Reason, "reason_code too long") || got.ReasonCode != "" {
		t.Fatalf("reason code rejection: %+v", got)
	}
}
