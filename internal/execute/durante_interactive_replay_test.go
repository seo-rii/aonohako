package execute

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

type discardReplayIO struct{ io.Writer }

func (discardReplayIO) Close() error { return nil }

func interactiveReplayFixture(t *testing.T, mode byte) *interactiveTranscript {
	t.Helper()
	trace := &interactiveTranscript{}
	judge := interactivePipeWriter{w: discardReplayIO{io.Discard}, transcript: trace}
	contestant := interactivePipeWriter{w: discardReplayIO{io.Discard}, transcript: trace, fromContestant: true}
	judge.Write([]byte{mode})
	contestant.Write([]byte("ready\n"))
	judge.Write([]byte{0xa5})
	judge.Close()
	contestant.Write([]byte("done\n"))
	contestant.Close()
	trace.seal(true)
	if !trace.ready() {
		t.Fatal("incomplete transcript fixture")
	}
	return trace
}

func TestInteractiveContestantReplayRequiresEligibleSubmission(t *testing.T) {
	svc := &Service{workMeasurement: observerConfig()}
	req := observerRequest()
	req.EnableNetwork = true
	if report, reason := svc.replayInteractiveContestant(t.Context(), req, interactiveReplayFixture(t, 'A'), pipelineOutputDigests(nil, nil), config.DefaultRuntimeTuningConfig()); report != nil || reason != "interactive contestant replay unavailable" {
		t.Fatalf("networked stage entered replay: %v, %q", report, reason)
	}
}

func TestDuranteInteractiveContestantReplayIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated Durante validation image required")
	}
	forceDirectMode(t)
	binary := buildCTestBinary(t, `
#include <stdio.h>
#include <unistd.h>
int main(void) {
    int mode = getchar();
    if (mode == 'S') sleep(12);
    puts(mode == 'D' ? "diverged" : "ready");
    fflush(stdout);
    if (getchar() != 0xa5 || getchar() != EOF) return 4;
    FILE *out = fopen("result.bin", "wb");
    if (!out) return 5;
    fputc(0,out); fputc(0xff,out);
    if (fclose(out) != 0) return 6;
    puts("done");
    return mode == 'E' ? 1 : 0;
}`)
	cfg := observerConfig()
	svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	single := &model.RunRequest{Lang: "CPP20", Binaries: []model.Binary{{Name: "Main", DataB64: binary, Mode: "exec"}},
		Limits: model.Limits{TimeMs: 1, WallTimeMs: 5, MemoryMB: 64}, FileOutputs: []model.OutputFile{{Path: "result.bin"}},
		DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX}}
	for _, tc := range []struct {
		name         string
		mode         byte
		stdout       string
		expectedFile []byte
		wantReason   string
	}{
		{"binary dialogue and output file", 'A', "ready\ndone\n", []byte{0, 0xff}, ""},
		{"changed native stdout", 'A', "different\n", []byte{0, 0xff}, "interactive contestant replay output differs from native output"},
		{"changed file", 'A', "ready\ndone\n", []byte{0, 0xfe}, "interactive contestant replay output differs from native output"},
		{"changed dialogue", 'D', "ready\ndone\n", []byte{0, 0xff}, "interactive contestant replay did not match the native exchange"},
		{"failed process after matching output", 'E', "ready\ndone\n", []byte{0, 0xff}, "interactive contestant replay did not produce a complete compatible measurement"},
		{"raw wall budget", 'S', "ready\ndone\n", []byte{0, 0xff}, "interactive contestant replay did not match the native exchange"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := *svc
			if tc.mode == 'S' {
				p := *cfg.ResourcePolicy
				p.MaxLimits.CPUTimeMs, p.MaxLimits.WallTimeMs = 1000, 1000
				worker.workMeasurement.ResourcePolicy = &p
			}
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			report, reason := worker.replayInteractiveContestant(ctx, single, interactiveReplayFixture(t, tc.mode), pipelineOutputDigests([]byte(tc.stdout), tc.expectedFile), config.DefaultRuntimeTuningConfig())
			if ctx.Err() != nil {
				t.Fatal("replay outlived the raw resource deadline")
			}
			if (report != nil) != (tc.wantReason == "") || reason != tc.wantReason {
				t.Fatalf("got report=%v reason=%q", report != nil, reason)
			}
			if report != nil && !observationReportMatches(single, cfg, report) {
				t.Fatal("missing source-family or CPU-feature policy in report")
			}
		})
	}
}
