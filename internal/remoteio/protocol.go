package remoteio

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	ProtocolVersionHeader = "X-Aonohako-Protocol-Version"
	ProtocolVersion       = "2026-08-26"
)

func CheckProtocolVersion(headers http.Header) error {
	return CheckProtocolVersionWithPolicy(headers, false)
}

func CheckProtocolVersionStrict(headers http.Header) error {
	return CheckProtocolVersionWithPolicy(headers, true)
}

func CheckProtocolVersionWithPolicy(headers http.Header, requireHeader bool) error {
	got := strings.TrimSpace(headers.Get(ProtocolVersionHeader))
	if got == "" {
		if requireHeader {
			return fmt.Errorf("missing remote protocol version header %q; expected %q", ProtocolVersionHeader, ProtocolVersion)
		}
		return nil
	}
	if got == ProtocolVersion {
		return nil
	}
	return fmt.Errorf("unsupported remote protocol version %q; expected %q", got, ProtocolVersion)
}
