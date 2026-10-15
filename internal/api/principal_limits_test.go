package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/platform"
)

func TestPlatformLongPrincipalLimitsRemainIsolated(t *testing.T) {
	for _, mode := range []string{"signed", "trusted-dev-header"} {
		t.Run(mode, func(t *testing.T) {
			cfg := configForTest(t)
			cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
			if mode == "signed" {
				cfg.InboundAuth.PlatformPrincipalHMACSecret = "platform-secret"
			} else {
				cfg.Execution.Platform.DeploymentTarget = platform.DeploymentTargetDev
				cfg.TrustedPlatformHeaders = true
				cfg.TrustedPlatformHeaderCIDRs = []string{"127.0.0.1/32"}
			}
			cfg.MaxPrincipalStreams = 1
			cfg.MaxPrincipalRequestsPerMinute = 1
			s := NewWithServices(cfg, compile.New(), execute.New())
			prefix := strings.Repeat("same-prefix-", 30)
			values := []string{prefix + "alice", prefix + "bob", prefix + "alice"}
			var keys []string
			handler := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, principalFromContext(r.Context()))
				w.WriteHeader(http.StatusNoContent)
			}))
			for i, value := range values {
				body := []byte(`{}`)
				req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
				req.RemoteAddr = "127.0.0.1:1234"
				req.Header.Set(platformPrincipalHeader, value)
				if mode == "signed" {
					timestamp := time.Now().UTC().Format(time.RFC3339)
					nonce := platformPrincipalNonceForTest(i + 1)
					req.Header.Set(platformPrincipalTimestampHeader, timestamp)
					req.Header.Set(platformPrincipalNonceHeader, nonce)
					req.Header.Set(platformPrincipalSignatureHeader, platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", value, timestamp, nonce, body))
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code != http.StatusNoContent {
					t.Fatalf("authentication status = %d, want 204; body=%s", rr.Code, rr.Body.String())
				}
			}
			if keys[0] == keys[1] {
				t.Fatal("different principals with a long common prefix share a usage key")
			}
			if keys[0] != keys[2] {
				t.Fatal("the same principal has different usage keys across requests")
			}
			for _, key := range keys {
				if len(key) > 240 || !strings.HasPrefix(key, "platform:") {
					t.Fatalf("platform usage key has an invalid length or namespace: %q", key)
				}
			}

			for _, key := range keys[:2] {
				release, ok, code := s.acquireStream(key)
				if !ok {
					t.Fatalf("a different long principal cannot acquire a stream: %s", code)
				}
				t.Cleanup(release)
			}
			if release, ok, code := s.acquireStream(keys[2]); ok {
				release()
				t.Fatal("the same long principal bypassed its active stream cap")
			} else if code != "principal_stream_limit_exceeded" {
				t.Fatalf("stream rejection code = %q", code)
			}

			now := time.Now()
			if !s.allowPrincipalRequest(keys[0], now) || !s.allowPrincipalRequest(keys[1], now) {
				t.Fatal("different long principals share a request-rate limit")
			}
			if s.allowPrincipalRequest(keys[2], now) {
				t.Fatal("the same long principal bypassed its request-rate cap")
			}
		})
	}
}

func TestPlatformLongClaimedPrincipalUploadLimitsRemainIsolated(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
	cfg.MaxPrincipalUploads = 1
	s := NewWithServices(cfg, compile.New(), execute.New())
	prefix := strings.Repeat("same-prefix-", 30)
	var keys []string
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
		if s.uploads.Load() != 0 || len(s.principalUploads) != 0 {
			t.Error("upload admission retained counters after release")
		}
	}()
	for _, suffix := range []string{"alice", "bob", "alice"} {
		req := httptest.NewRequest(http.MethodPost, "/execute", nil)
		req.Header.Set(platformPrincipalHeader, prefix+suffix)
		keys = append(keys, s.preAuthPrincipal(req))
	}
	for _, key := range keys[:2] {
		release, ok, code := s.acquireUpload(key)
		if !ok {
			t.Fatalf("a different long claimed principal cannot acquire upload admission: %s", code)
		}
		releases = append(releases, release)
	}
	if release, ok, code := s.acquireUpload(keys[2]); ok {
		release()
		t.Fatal("the same long claimed principal bypassed its upload cap")
	} else if code != "principal_upload_limit_exceeded" {
		t.Fatalf("upload rejection code = %q", code)
	}
}
