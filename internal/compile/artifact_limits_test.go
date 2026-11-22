package compile

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func Test32MiBArtifactAcceptedLocallyAndRemotely(t *testing.T) {
	const artifactBytes = 32 << 20
	root := t.TempDir()
	path := filepath.Join(root, "Main")
	if err := os.WriteFile(path, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, artifactBytes); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectArtifacts(root, nil, "")
	if err != nil {
		t.Fatalf("collect 32 MiB artifact: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "Main" || artifacts[0].Mode != "exec" {
		t.Fatalf("collected artifacts: count=%d", len(artifacts))
	}
	data := artifacts[0].DataB64
	if len(data) != base64.StdEncoding.EncodedLen(artifactBytes) {
		t.Fatalf("encoded bytes = %d, want %d", len(data), base64.StdEncoding.EncodedLen(artifactBytes))
	}

	// Stream the real collected payload without constructing another large JSON
	// buffer, while exercising the remote SSE parser and decoded artifact limit.
	client := &http.Client{Transport: compileRoundTripFunc(func(*http.Request) (*http.Response, error) {
		body := io.MultiReader(
			strings.NewReader("event: result\ndata: {\"status\":\"OK\",\"artifacts\":[{\"name\":\"Main\",\"mode\":\"exec\",\"data_b64\":\""),
			strings.NewReader(data),
			strings.NewReader("\"}]}\n\n"),
		)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(body),
		}, nil
	})}
	runner := &remoteRunner{client: client, compileURL: "http://compiler.invalid/compile", idleTimeout: -1}
	response := runner.Run(context.Background(), &model.CompileRequest{Lang: "HASKELL"})
	if response.Status != model.CompileStatusOK {
		t.Fatalf("receive 32 MiB artifact: status=%s reason=%s", response.Status, response.Reason)
	}
	if len(response.Artifacts) != 1 || response.Artifacts[0] != artifacts[0] {
		t.Fatal("remote compile changed the collected artifact")
	}
}
