package execute

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/remoteio"
	"aonohako/internal/runvalidation"
)

// SupportsPipelineV1 checks the worker at the same configured origin and path
// prefix used for execution. Credential retrieval shares the probe's deadline.
func (r *remoteRunner) SupportsPipelineV1(ctx context.Context) bool {
	switch r.auth {
	case config.RemoteAuthBearer:
		if strings.TrimSpace(r.bearerToken) == "" {
			return false
		}
	case config.RemoteAuthCloudRunIDToken:
	default:
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	executeURL, err := url.Parse(r.executeURL)
	if err != nil {
		return false
	}
	endpoint := executeURL.ResolveReference(&url.URL{Path: strings.TrimPrefix(model.PipelineV1WorkerPath, "/")})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return false
	}
	auth, err := r.authorizationHeader(ctx)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || err != nil || contentType != "application/json" {
		return false
	}
	body, err := remoteio.ReadBoundedBody(resp.Body, 4096)
	if err != nil {
		return false
	}
	var worker model.PipelineV1WorkerCapability
	return json.Unmarshal(body, &worker) == nil && worker.Protocol == model.PipelineV1WorkerProtocol &&
		runvalidation.SupportsPipelineV1Contract(worker.Pipeline)
}
