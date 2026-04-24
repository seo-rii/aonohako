package remoteio

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	ProtocolVersionHeader = "X-Aonohako-Protocol-Version"
	ProtocolVersion       = "2026-04-24"
)

func CheckProtocolVersion(headers http.Header) error {
	got := strings.TrimSpace(headers.Get(ProtocolVersionHeader))
	if got == "" || got == ProtocolVersion {
		return nil
	}
	return fmt.Errorf("unsupported remote protocol version %q; expected %q", got, ProtocolVersion)
}
