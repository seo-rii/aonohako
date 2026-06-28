package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

const payloadURLDownloadTimeout = 60 * time.Second

var payloadURLHTTPClient = &http.Client{
	Timeout: payloadURLDownloadTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return validatePayloadURL(req.URL.String())
	},
}

func resolveCompilePayloadURLs(ctx context.Context, req *model.CompileRequest) error {
	for i := range req.Sources {
		if err := resolveBase64URL(ctx, fmt.Sprintf("sources[%d]", i), &req.Sources[i].DataB64, req.Sources[i].DataURL, maxCompileDecodedSourceBytes); err != nil {
			return err
		}
	}
	return nil
}

func resolveRunPayloadURLs(ctx context.Context, req *model.RunRequest) error {
	if err := validateOptionalPayloadURL(req.StdinURL); err != nil {
		return fmt.Errorf("stdin_url: %w", err)
	}
	if err := resolveTextURL(ctx, "expected_stdout", &req.ExpectedStdout, &req.ExpectedStdoutURL, runvalidation.MaxTextFieldBytes); err != nil {
		return err
	}
	if err := resolveBinaryURLs(ctx, "binaries", req.Binaries); err != nil {
		return err
	}
	for i := range req.Programs {
		if err := resolveBinaryURLs(ctx, fmt.Sprintf("programs[%d].binaries", i), req.Programs[i].Binaries); err != nil {
			return err
		}
	}
	for i := range req.Steps {
		if err := validateOptionalPayloadURL(req.Steps[i].StdinURL); err != nil {
			return fmt.Errorf("steps[%d].stdin_url: %w", i, err)
		}
		for j := range req.Steps[i].StdinParts {
			if err := validateOptionalPayloadURL(req.Steps[i].StdinParts[j].DataURL); err != nil {
				return fmt.Errorf("steps[%d].stdin_parts[%d].data_url: %w", i, j, err)
			}
		}
	}
	if req.SPJ != nil && req.SPJ.Binary != nil {
		if err := resolveBase64URL(ctx, "spj.binary", &req.SPJ.Binary.DataB64, req.SPJ.Binary.DataURL, runvalidation.MaxBinaryFileBytes); err != nil {
			return err
		}
	}
	if req.Interactor != nil {
		if err := resolveBinaryURLs(ctx, "interactor.binaries", req.Interactor.Binaries); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionalPayloadURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return nil
	}
	return validatePayloadURL(rawURL)
}

func resolveBinaryURLs(ctx context.Context, label string, binaries []model.Binary) error {
	for i := range binaries {
		if err := resolveBase64URL(ctx, fmt.Sprintf("%s[%d]", label, i), &binaries[i].DataB64, binaries[i].DataURL, runvalidation.MaxBinaryFileBytes); err != nil {
			return err
		}
	}
	return nil
}

func resolveTextURL(ctx context.Context, label string, inline *string, rawURL *string, maxBytes int) error {
	if rawURL == nil || strings.TrimSpace(*rawURL) == "" {
		return nil
	}
	if inline != nil && *inline != "" {
		return fmt.Errorf("%s cannot combine inline content with url", label)
	}
	data, err := downloadPayloadURL(ctx, *rawURL, maxBytes)
	if err != nil {
		return fmt.Errorf("%s_url: %w", label, err)
	}
	if inline != nil {
		*inline = string(data)
	}
	*rawURL = ""
	return nil
}

func resolveBase64URL(ctx context.Context, label string, inline *string, rawURL string, maxBytes int) error {
	if strings.TrimSpace(rawURL) == "" {
		return nil
	}
	if inline != nil && *inline != "" {
		return fmt.Errorf("%s cannot combine data_b64 with data_url", label)
	}
	data, err := downloadPayloadURL(ctx, rawURL, maxBytes)
	if err != nil {
		return fmt.Errorf("%s.data_url: %w", label, err)
	}
	if inline != nil {
		*inline = base64.StdEncoding.EncodeToString(data)
	}
	return nil
}

func downloadPayloadURL(ctx context.Context, rawURL string, maxBytes int) ([]byte, error) {
	if err := validatePayloadURL(rawURL); err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "aonohako-url-payload/1")
	resp, err := payloadURLHTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.ContentLength > int64(maxBytes) {
		return nil, fmt.Errorf("download too large: max %d bytes", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("download too large: max %d bytes", maxBytes)
	}
	return data, nil
}

func validatePayloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("url scheme must be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("url host is required")
	}
	return nil
}
