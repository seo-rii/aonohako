package runtimepacks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRuntimeDockerfileDeclaresRuntimeBaseBeforeFirstFrom(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	argIndex := strings.Index(body, "ARG RUNTIME_BASE=")
	goFromIndex := strings.Index(body, "FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder")
	runtimeFromIndex := strings.Index(body, "FROM ${RUNTIME_BASE} AS runtime")
	if argIndex == -1 || goFromIndex == -1 || runtimeFromIndex == -1 {
		t.Fatalf("runtime.Dockerfile is missing expected markers")
	}
	if !(argIndex < goFromIndex && goFromIndex < runtimeFromIndex) {
		t.Fatalf("ARG RUNTIME_BASE must be declared before the first FROM to be usable in a later FROM")
	}
}

func TestRuntimeDockerfileUsesGo123BuilderImage(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	m := regexp.MustCompile(`ARG GO_IMAGE=golang:(\d+\.\d+)-bookworm`).FindStringSubmatch(string(data))
	if len(m) != 2 {
		t.Fatalf("runtime.Dockerfile is missing a parseable GO_IMAGE default")
	}
	if m[1] != "1.23" {
		t.Fatalf("GO_IMAGE default = %s, want 1.23 to satisfy go.mod and CI image builds", m[1])
	}
}

func TestRuntimeDockerfilePATHIncludesSbin(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin") {
		t.Fatalf("runtime.Dockerfile PATH must include /usr/sbin and /sbin for sandbox tools")
	}
}
