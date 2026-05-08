package remoteio

import (
	"net/http"
	"strings"
	"testing"
)

func TestCheckProtocolVersionAllowsMissingOrCurrentHeader(t *testing.T) {
	if err := CheckProtocolVersion(http.Header{}); err != nil {
		t.Fatalf("missing header should remain backward-compatible: %v", err)
	}

	headers := http.Header{}
	headers.Set(ProtocolVersionHeader, ProtocolVersion)
	if err := CheckProtocolVersion(headers); err != nil {
		t.Fatalf("current protocol version should be accepted: %v", err)
	}
}

func TestCheckProtocolVersionStrictRejectsMissingHeader(t *testing.T) {
	err := CheckProtocolVersionStrict(http.Header{})
	if err == nil || !strings.Contains(err.Error(), "missing remote protocol version header") {
		t.Fatalf("expected missing protocol header error, got %v", err)
	}

	headers := http.Header{}
	headers.Set(ProtocolVersionHeader, ProtocolVersion)
	if err := CheckProtocolVersionStrict(headers); err != nil {
		t.Fatalf("current protocol version should be accepted in strict mode: %v", err)
	}
}

func TestCheckProtocolVersionRejectsMismatchedHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set(ProtocolVersionHeader, "1900-01-01")
	err := CheckProtocolVersion(headers)
	if err == nil || !strings.Contains(err.Error(), "unsupported remote protocol version") {
		t.Fatalf("expected protocol mismatch error, got %v", err)
	}
}
