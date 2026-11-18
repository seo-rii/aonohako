package execute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

func TestRemoteURLShadowRequiresSharedInputReceipt(t *testing.T) {
	cfg := judgeMeasurementConfig()
	req := shadowRequest()
	req.StdinURL = "http://payload.example/input"
	primary := model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 7, VerdictSource: "stdout", Stdout: "ordinary output"}
	shadow := shadowJudgeResponse(t, cfg, 20_000_000, model.RunStatusAccepted)
	for _, tc := range []struct {
		digest, agreement string
	}{
		{strings.Repeat("a1", 32), model.ShadowDisagree},
		{"", model.ShadowUnavailable}, // legacy runner that re-fetched stdin_url
		{strings.Repeat("a", 62), model.ShadowUnavailable},
		{strings.Repeat("G", 64), model.ShadowUnavailable},
		{strings.Repeat("A", 64), model.ShadowUnavailable},
	} {
		local := attachShadow(primary, &shadow, "")
		local.WorkMeasurementShadow.StdinSHA256 = tc.digest
		got := exchangeTimeMeasurementResult(t, req, local, cfg)
		if got.Status != primary.Status || got.CPUTimeMs != primary.CPUTimeMs || got.Stdout != primary.Stdout ||
			got.WorkMeasurementShadow == nil || got.WorkMeasurementShadow.Agreement != tc.agreement {
			t.Errorf("digest %q: %+v", tc.digest, got)
		}
	}
}

func TestShadowURLInputAdmissionAndFailure(t *testing.T) {
	shortCaseTestWorkRoot(t)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write([]byte{0, 0xff, 0x80, 1, 2})
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: judgeMeasurementConfig()}})
	req := shadowRequest()
	req.StdinURL = "http://payload.example/input"
	req.Limits.WorkspaceBytes = 4
	got := svc.Run(context.Background(), req, Hooks{})
	if downloads.Load() != 0 || got.Status != model.RunStatusInitFail || got.Reason != "no binaries" || got.WorkMeasurementShadow == nil || got.WorkMeasurementShadow.Agreement != model.ShadowUnavailable {
		t.Fatalf("downloads=%d, primary admission: %+v", downloads.Load(), got)
	}
	req.Binaries = []model.Binary{{Name: "Main", Mode: "exec", DataB64: b64("unused")}}
	got = svc.Run(context.Background(), req, Hooks{})
	if downloads.Load() != 1 || got.Status != model.RunStatusInitFail || !strings.Contains(got.Reason, "too large") || got.WorkMeasurementShadow == nil || got.WorkMeasurementShadow.Agreement != model.ShadowUnavailable || got.WorkMeasurementShadow.Response != nil {
		t.Fatalf("downloads=%d, failed URL capture: %+v", downloads.Load(), got)
	}
}
