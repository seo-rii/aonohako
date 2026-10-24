package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"aonohako/internal/entrypolicy"
	"aonohako/internal/platform"
)

func TestEntryCapabilityRequiresInstalledRunner(t *testing.T) {
	// Catalog metadata alone must not make an older or incomplete image accept
	// Entry submissions. This must also hold when its Node executable is absent.
	t.Setenv("AONOHAKO_LANGUAGES", "entry")
	t.Setenv("PATH", t.TempDir())
	server := newServerForTest(t)
	for _, transport := range []platform.ExecutionTransport{
		platform.ExecutionTransportEmbedded,
		platform.ExecutionTransportRemote,
	} {
		server.cfg.Execution.Platform.ExecutionTransport = transport
		recorder := httptest.NewRecorder()
		server.capabilities(recorder, httptest.NewRequest(http.MethodGet, "/capabilities", nil))
		var contract struct {
			Capabilities []string        `json:"capabilities"`
			Entry        json.RawMessage `json:"entry"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &contract); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(contract.Capabilities, entrypolicy.Capability) || len(contract.Entry) != 0 {
			t.Fatalf("unavailable runtime was advertised: %s", recorder.Body.String())
		}
	}
}
