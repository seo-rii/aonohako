package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/payloadurl"
	"aonohako/internal/runvalidation"
)

const (
	payloadURLDownloadTimeout = 60 * time.Second
	payloadURLRequestTimeout  = 60 * time.Second
)

var payloadURLHTTPClient = payloadurl.NewHTTPClient(payloadURLDownloadTimeout)

type payloadDownloadTooLargeError struct {
	maxBytes int
}

func (e *payloadDownloadTooLargeError) Error() string {
	return fmt.Sprintf("download too large: max %d bytes", e.maxBytes)
}

type payloadByteBudget struct {
	remaining int
	limit     int
	message   string
}

func resolveCompilePayloadURLs(ctx context.Context, req *model.CompileRequest, decodedBytes int) error {
	if req == nil || !compileHasPayloadURLs(req) {
		return nil
	}
	for i := range req.Sources {
		source := &req.Sources[i]
		if strings.TrimSpace(source.DataURL) == "" {
			continue
		}
		if source.DataB64 != "" {
			return fmt.Errorf("sources[%d] cannot combine data_b64 with data_url", i)
		}
		if err := validatePayloadURL(source.DataURL); err != nil {
			return fmt.Errorf("sources[%d].data_url: %w", i, err)
		}
	}

	budget := &payloadByteBudget{
		remaining: maxCompileDecodedSourceTotalBytes - decodedBytes,
		limit:     maxCompileDecodedSourceTotalBytes,
		message:   "sources total size exceeded",
	}
	for i := range req.Sources {
		if err := resolveBase64URL(ctx, fmt.Sprintf("sources[%d]", i), &req.Sources[i].DataB64, &req.Sources[i].DataURL, maxCompileDecodedSourceBytes, budget); err != nil {
			return err
		}
	}
	return nil
}

func resolveRunPayloadURLs(ctx context.Context, req *model.RunRequest) error {
	if req == nil {
		return nil
	}
	// Validate every URL and inline/URL conflict before issuing the first
	// request, so a structurally invalid later field cannot cause earlier fetches.
	if err := validateOptionalPayloadURL(req.StdinURL); err != nil {
		return fmt.Errorf("stdin_url: %w", err)
	}
	if strings.TrimSpace(req.ExpectedStdoutURL) != "" {
		if req.ExpectedStdout != "" {
			return fmt.Errorf("expected_stdout cannot combine inline content with url")
		}
		if err := validatePayloadURL(req.ExpectedStdoutURL); err != nil {
			return fmt.Errorf("expected_stdout_url: %w", err)
		}
	}
	type binaryGroup struct {
		label      string
		binaries   []model.Binary
		singleItem bool
	}
	binaryGroups := []binaryGroup{{label: "binaries", binaries: req.Binaries}}
	for i := range req.Programs {
		binaryGroups = append(binaryGroups, binaryGroup{label: fmt.Sprintf("programs[%d].binaries", i), binaries: req.Programs[i].Binaries})
	}
	for i := range req.Steps {
		if err := validateOptionalPayloadURL(req.Steps[i].StdinURL); err != nil {
			return fmt.Errorf("steps[%d].stdin_url: %w", i, err)
		}
		for j := range req.Steps[i].StdinParts {
			part := req.Steps[i].StdinParts[j]
			if strings.TrimSpace(part.DataURL) != "" && part.Data != "" {
				return fmt.Errorf("steps[%d].stdin_parts[%d] cannot combine data with data_url", i, j)
			}
			if err := validateOptionalPayloadURL(part.DataURL); err != nil {
				return fmt.Errorf("steps[%d].stdin_parts[%d].data_url: %w", i, j, err)
			}
		}
	}
	if req.SPJ != nil && req.SPJ.Binary != nil {
		binaryGroups = append(binaryGroups, binaryGroup{label: "spj.binary", binaries: []model.Binary{*req.SPJ.Binary}, singleItem: true})
	}
	if req.Interactor != nil {
		binaryGroups = append(binaryGroups, binaryGroup{label: "interactor.binaries", binaries: req.Interactor.Binaries})
	}
	for _, group := range binaryGroups {
		for i := range group.binaries {
			if strings.TrimSpace(group.binaries[i].DataURL) == "" {
				continue
			}
			field := fmt.Sprintf("%s[%d]", group.label, i)
			if group.singleItem {
				field = group.label
			}
			if group.binaries[i].DataB64 != "" {
				return fmt.Errorf("%s cannot combine data_b64 with data_url", field)
			}
			if err := validatePayloadURL(group.binaries[i].DataURL); err != nil {
				return fmt.Errorf("%s.data_url: %w", field, err)
			}
		}
	}
	if !runHasBufferedPayloadURLs(req) {
		return nil
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
	if req.SPJ != nil && req.SPJ.Binary != nil {
		if err := resolveBase64URL(ctx, "spj.binary", &req.SPJ.Binary.DataB64, &req.SPJ.Binary.DataURL, runvalidation.MaxBinaryFileBytes, nil); err != nil {
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

func compileHasPayloadURLs(req *model.CompileRequest) bool {
	if req == nil {
		return false
	}
	for _, source := range req.Sources {
		if strings.TrimSpace(source.DataURL) != "" {
			return true
		}
	}
	return false
}

func runHasBufferedPayloadURLs(req *model.RunRequest) bool {
	if req == nil {
		return false
	}
	if strings.TrimSpace(req.ExpectedStdoutURL) != "" {
		return true
	}
	for _, binary := range req.Binaries {
		if strings.TrimSpace(binary.DataURL) != "" {
			return true
		}
	}
	for _, program := range req.Programs {
		for _, binary := range program.Binaries {
			if strings.TrimSpace(binary.DataURL) != "" {
				return true
			}
		}
	}
	if req.SPJ != nil && req.SPJ.Binary != nil && strings.TrimSpace(req.SPJ.Binary.DataURL) != "" {
		return true
	}
	if req.Interactor != nil {
		for _, binary := range req.Interactor.Binaries {
			if strings.TrimSpace(binary.DataURL) != "" {
				return true
			}
		}
	}
	return false
}

func validateOptionalPayloadURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return nil
	}
	return validatePayloadURL(rawURL)
}

func resolveBinaryURLs(ctx context.Context, label string, binaries []model.Binary) error {
	decodedBytes := 0
	for i := range binaries {
		if strings.TrimSpace(binaries[i].DataURL) != "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(binaries[i].DataB64)
		if err != nil {
			return fmt.Errorf("%s[%d].data_b64 invalid base64: %w", label, i, err)
		}
		decodedBytes += len(data)
	}
	if decodedBytes > runvalidation.MaxBinaryTotalBytes {
		return fmt.Errorf("%s total size exceeded: max %d bytes", label, runvalidation.MaxBinaryTotalBytes)
	}
	budget := &payloadByteBudget{
		remaining: runvalidation.MaxBinaryTotalBytes - decodedBytes,
		limit:     runvalidation.MaxBinaryTotalBytes,
		message:   label + " total size exceeded",
	}
	for i := range binaries {
		if err := resolveBase64URL(ctx, fmt.Sprintf("%s[%d]", label, i), &binaries[i].DataB64, &binaries[i].DataURL, runvalidation.MaxBinaryFileBytes, budget); err != nil {
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

func resolveBase64URL(ctx context.Context, label string, inline, rawURL *string, maxBytes int, budget *payloadByteBudget) error {
	if rawURL == nil || strings.TrimSpace(*rawURL) == "" {
		return nil
	}
	if inline != nil && *inline != "" {
		return fmt.Errorf("%s cannot combine data_b64 with data_url", label)
	}
	downloadLimit := maxBytes
	if budget != nil && budget.remaining < downloadLimit {
		downloadLimit = max(0, budget.remaining)
	}
	data, err := downloadPayloadURL(ctx, *rawURL, downloadLimit)
	if err != nil {
		var tooLarge *payloadDownloadTooLargeError
		if budget != nil && downloadLimit < maxBytes && errors.As(err, &tooLarge) {
			return fmt.Errorf("%s: max %d bytes", budget.message, budget.limit)
		}
		return fmt.Errorf("%s.data_url: %w", label, err)
	}
	if budget != nil {
		if len(data) > budget.remaining {
			return fmt.Errorf("%s: max %d bytes", budget.message, budget.limit)
		}
		budget.remaining -= len(data)
	}
	if inline != nil {
		*inline = base64.StdEncoding.EncodeToString(data)
	}
	*rawURL = ""
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
		return nil, &payloadDownloadTooLargeError{maxBytes: maxBytes}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, &payloadDownloadTooLargeError{maxBytes: maxBytes}
	}
	return data, nil
}

func validatePayloadURL(rawURL string) error {
	return payloadurl.Validate(rawURL)
}
