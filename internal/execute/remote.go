package execute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/remoteio"
)

const cloudRunMetadataIdentityURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity"

type remoteRunner struct {
	client      *http.Client
	executeURL  string
	auth        config.RemoteAuthMode
	bearerToken string
	audience    string
	metadataURL string
	idleTimeout time.Duration
}

func newRemoteRunner(cfg config.Config) Runner {
	auth := cfg.Execution.Remote.Auth
	if auth == "" {
		auth = config.RemoteAuthNone
	}
	return &remoteRunner{
		client:      remoteio.NewHTTPClient(),
		executeURL:  normalizeRemoteExecuteURL(cfg.Execution.Remote.URL),
		auth:        auth,
		bearerToken: cfg.Execution.Remote.BearerToken,
		audience:    cfg.Execution.Remote.Audience,
		metadataURL: cloudRunMetadataIdentityURL,
		idleTimeout: cfg.Execution.Remote.SSEIdleTimeout,
	}
}

func (r *remoteRunner) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote request encode failed: " + err.Error()}
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, r.executeURL, bytes.NewReader(body))
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote request build failed: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	if authHeader, err := r.authorizationHeader(ctx); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote auth failed: " + err.Error()}
	} else if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		reason := strings.TrimSpace(string(raw))
		if reason == "" {
			reason = resp.Status
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err == nil {
			if msg, ok := payload["error"].(string); ok && strings.TrimSpace(msg) != "" {
				reason = msg
			}
		}
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote execute returned %s: %s", resp.Status, reason)}
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote execute returned unexpected content type: %s", contentType)}
	}
	if err := remoteio.CheckProtocolVersion(resp.Header); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute protocol mismatch: " + err.Error()}
	}

	reader := remoteio.NewSSEReader(resp.Body)
	idleTimeout := r.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = remoteio.DefaultSSEIdleTimeout
	}
	if idleTimeout > 0 {
		idleTimer := time.AfterFunc(idleTimeout, cancelStream)
		defer idleTimer.Stop()
		reader.SetActivityCallback(func() {
			idleTimer.Reset(idleTimeout)
		})
	}
	result := model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute stream ended without result"}

	for {
		event, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return result
			}
			if streamCtx.Err() != nil && ctx.Err() == nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute stream idle timeout exceeded"}
			}
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute stream failed: " + err.Error()}
		}
		switch event.Name {
		case "log":
			if hooks.OnLog == nil {
				continue
			}
			var chunk struct {
				Stream string `json:"stream"`
				Chunk  string `json:"chunk"`
			}
			if err := json.Unmarshal([]byte(event.Data), &chunk); err == nil && chunk.Stream != "" {
				hooks.OnLog(chunk.Stream, chunk.Chunk)
			}
		case "image":
			if hooks.OnImage == nil {
				continue
			}
			var image struct {
				Mime string `json:"mime"`
				B64  string `json:"b64"`
				TS   int64  `json:"ts"`
			}
			if err := json.Unmarshal([]byte(event.Data), &image); err == nil && image.Mime != "" && image.B64 != "" {
				ts := image.TS
				if ts == 0 {
					ts = time.Now().UnixMilli()
				}
				hooks.OnImage(image.Mime, image.B64, ts)
			}
		case "error":
			var remoteErr struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(event.Data), &remoteErr); err == nil && strings.TrimSpace(remoteErr.Message) != "" {
				result.Reason = remoteErr.Message
			}
		case "result":
			if err := json.Unmarshal([]byte(event.Data), &result); err != nil {
				result = model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote result decode failed: " + err.Error()}
			}
			return result
		}
	}
}

func (r *remoteRunner) authorizationHeader(ctx context.Context) (string, error) {
	switch r.auth {
	case "", config.RemoteAuthNone:
		return "", nil
	case config.RemoteAuthBearer:
		return "Bearer " + r.bearerToken, nil
	case config.RemoteAuthCloudRunIDToken:
		query := url.Values{}
		query.Set("audience", r.audience)
		query.Set("format", "full")
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, r.metadataURL+"?"+query.Encode(), nil)
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Metadata-Flavor", "Google")
		resp, err := r.client.Do(httpReq)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("metadata server returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("metadata server returned an empty identity token")
		}
		return "Bearer " + token, nil
	default:
		return "", fmt.Errorf("unsupported remote auth mode: %s", r.auth)
	}
}

func normalizeRemoteExecuteURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return trimmed
	}
	if strings.HasSuffix(parsed.Path, "/execute") {
		return parsed.String()
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/execute"
		return parsed.String()
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/execute"
	return parsed.String()
}
