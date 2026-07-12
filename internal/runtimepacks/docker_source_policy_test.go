package runtimepacks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileSourcePolicy(t *testing.T) {
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	digestC := strings.Repeat("c", 64)
	digestD := strings.Repeat("d", 64)
	tests := []struct {
		name            string
		body            string
		allowedContexts []string
		wantErr         string
	}{
		{
			name:    "rejects mutable syntax frontend",
			body:    "# syntax=docker/dockerfile:1.7\nFROM scratch\n",
			wantErr: "unpinned syntax frontend",
		},
		{
			name:    "rejects mutable external copy source",
			body:    "FROM scratch\nCOPY --from=ubuntu:latest /etc/os-release /os-release\n",
			wantErr: "unpinned external COPY source",
		},
		{
			name:    "rejects undeclared named context",
			body:    "FROM scratch\nCOPY --from=aonohako-python-packages / /packages/\n",
			wantErr: "unpinned external COPY source",
		},
		{
			name:    "rejects mutable external run mount source",
			body:    "FROM scratch\nRUN --mount=type=bind,from=alpine:latest,target=/src true\n",
			wantErr: "unpinned external RUN mount source",
		},
		{
			name: "allows digest pinned external sources",
			body: "# syntax=docker/dockerfile:1.7@sha256:" + digestA + "\n" +
				"FROM alpine@sha256:" + digestB + " AS build\n" +
				"FROM scratch\n" +
				"COPY --from=build /bin/tool /bin/tool\n" +
				"COPY --from=debian@sha256:" + digestC + " /etc/os-release /os-release\n" +
				"RUN --mount=type=bind,from=busybox@sha256:" + digestD + ",target=/src true\n",
		},
		{
			name: "allows numeric and named stages",
			body: "FROM scratch AS build\nFROM scratch\nCOPY --from=0 /first /first\nCOPY --from=build /second /second\nRUN --mount=type=bind,from=build,target=/src true\n",
		},
		{
			name:            "allows explicitly declared named context",
			body:            "FROM scratch\nCOPY --from=aonohako-python-packages / /packages/\n",
			allowedContexts: []string{"aonohako-python-packages"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Dockerfile")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", path, err)
			}
			args := []string{filepath.Join("..", "..", "scripts", "check_dockerfile_bases.sh")}
			for _, contextName := range tc.allowedContexts {
				args = append(args, "--allow-context", contextName)
			}
			args = append(args, path)
			cmd := exec.Command("bash", args...)
			output, err := cmd.CombinedOutput()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("policy rejected valid Dockerfile: %v\n%s", err, output)
				}
				return
			}
			if err == nil {
				t.Fatalf("policy accepted invalid Dockerfile; output:\n%s", output)
			}
			if !strings.Contains(string(output), tc.wantErr) {
				t.Fatalf("policy output = %q, want substring %q", output, tc.wantErr)
			}
		})
	}
}
