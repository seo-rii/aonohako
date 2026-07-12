package runtimepacks

import (
	"strings"
	"testing"
)

func TestLoadCatalogRejectsAmbiguousOrIncompleteDocuments(t *testing.T) {
	validCatalog := `
languages:
  alpha:
    smoke:
      command: ["alpha", "--version"]
profiles:
  combined:
    base_image: debian:trixie-slim
    languages: [alpha]
`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown root field",
			body: validCatalog + "unknown_option: true\n",
			want: "field unknown_option not found",
		},
		{
			name: "unknown nested field",
			body: strings.Replace(validCatalog, "    smoke:\n", "    smoko:\n", 1),
			want: "field smoko not found",
		},
		{
			name: "trailing yaml document",
			body: validCatalog + "---\nlanguages: {}\n",
			want: "multiple YAML documents",
		},
		{
			name: "empty catalog",
			body: "{}\n",
			want: "at least one language",
		},
		{
			name: "missing profiles",
			body: `
languages:
  alpha:
    smoke:
      command: ["alpha", "--version"]
profiles: {}
`,
			want: "at least one profile",
		},
		{
			name: "empty profile languages",
			body: `
languages:
  alpha:
    smoke:
      command: ["alpha", "--version"]
profiles:
  combined:
    base_image: debian:trixie-slim
    languages: []
`,
			want: "profile combined must contain at least one language",
		},
		{
			name: "missing smoke command",
			body: `
languages:
  alpha: {}
profiles:
  combined:
    base_image: debian:trixie-slim
    languages: [alpha]
`,
			want: "language alpha must define a smoke command",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCatalog(writeCatalogFixture(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadCatalog error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
