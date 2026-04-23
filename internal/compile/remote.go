package compile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

const cloudRunMetadataIdentityURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity"

type remoteRunner struct {
	client      *http.Client
	compileURL  string
	auth        config.RemoteAuthMode
	bearerToken string
	audience    string
	metadataURL string
}

func newRemoteRunner(cfg config.Config) Runner {
	auth := cfg.Execution.Remote.Auth
	if auth == "" {
		auth = config.RemoteAuthNone
	}
	return &remoteRunner{
		client:      &http.Client{},
		compileURL:  normalizeRemoteCompileURL(cfg.Execution.Remote.URL),
		auth:        auth,
		bearerToken: cfg.Execution.Remote.BearerToken,
		audience:    cfg.Execution.Remote.Audience,
		metadataURL: cloudRunMetadataIdentityURL,
	}
}

func (r *remoteRunner) Run(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
	if req == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote request encode failed: " + err.Error()}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.compileURL, bytes.NewReader(body))
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote request build failed: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	authHeader, err := r.authorizationHeader(ctx)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote auth failed: " + err.Error()}
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote compile request failed: " + err.Error()}
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
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: fmt.Sprintf("remote compile returned %s: %s", resp.Status, reason)}
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: fmt.Sprintf("remote compile returned unexpected content type: %s", contentType)}
	}

	reader := bufio.NewReader(resp.Body)
	eventName := ""
	dataLines := make([]string, 0, 4)
	result := model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote compile stream ended without result"}

	dispatch := func() bool {
		if eventName == "" {
			dataLines = dataLines[:0]
			return false
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		switch eventName {
		case "error":
			var remoteErr struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(payload), &remoteErr); err == nil && strings.TrimSpace(remoteErr.Message) != "" {
				result.Reason = remoteErr.Message
			}
		case "result":
			if err := json.Unmarshal([]byte(payload), &result); err != nil {
				result = model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote result decode failed: " + err.Error()}
			}
			return true
		}
		return false
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "remote compile stream failed: " + err.Error()}
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if dispatch() {
				return result
			}
			eventName = ""
		} else if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if err == io.EOF {
			dispatch()
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

func normalizeRemoteCompileURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return trimmed
	}
	if strings.HasSuffix(parsed.Path, "/compile") {
		return parsed.String()
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/compile"
		return parsed.String()
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/compile"
	return parsed.String()
}
