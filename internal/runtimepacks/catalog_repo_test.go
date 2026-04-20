package runtimepacks

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestRepositoryCatalogIncludesPlainRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	production, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	if len(production) != 9 {
		t.Fatalf("expected 9 production images, got %d", len(production))
	}

	if production[0].Name != "type-a" || !reflect.DeepEqual(production[0].Languages, []string{"bf", "elixir", "haskell", "lua", "ocaml", "perl", "php", "plain", "pypy", "python", "ruby", "sqlite", "wasm", "whitespace"}) {
		t.Fatalf("type-a production image = %+v", production[0])
	}
	if production[1].Name != "type-b" || !reflect.DeepEqual(production[1].Languages, []string{"java", "javascript", "scala", "typescript"}) {
		t.Fatalf("type-b production image = %+v", production[1])
	}
	if production[2].Name != "type-c" || !reflect.DeepEqual(production[2].Languages, []string{"go", "rust"}) {
		t.Fatalf("type-c production image = %+v", production[2])
	}
	if production[3].Name != "type-d" || !reflect.DeepEqual(production[3].Languages, []string{"kotlin"}) {
		t.Fatalf("type-d production image = %+v", production[3])
	}
	if production[4].Name != "type-e" || !reflect.DeepEqual(production[4].Languages, []string{"csharp", "fsharp"}) {
		t.Fatalf("type-e production image = %+v", production[4])
	}
	if production[5].Name != "type-f" || !reflect.DeepEqual(production[5].Languages, []string{"uhmlang"}) {
		t.Fatalf("type-f production image = %+v", production[5])
	}
	if production[6].Name != "type-g" || !reflect.DeepEqual(production[6].Languages, []string{"julia"}) {
		t.Fatalf("type-g production image = %+v", production[6])
	}
	if production[7].Name != "type-h" || !reflect.DeepEqual(production[7].Languages, []string{"swift"}) {
		t.Fatalf("type-h production image = %+v", production[7])
	}
	if production[8].Name != "type-i" || !reflect.DeepEqual(production[8].Languages, []string{"java", "plain", "python"}) {
		t.Fatalf("type-i production image = %+v", production[8])
	}

	ci, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}
	names := make([]string, 0, len(ci))
	for _, spec := range ci {
		names = append(names, spec.Name)
	}
	if !reflect.DeepEqual(names, []string{
		"ci-bf",
		"ci-csharp",
		"ci-elixir",
		"ci-fsharp",
		"ci-go",
		"ci-haskell",
		"ci-java",
		"ci-javascript",
		"ci-julia",
		"ci-kotlin",
		"ci-lua",
		"ci-ocaml",
		"ci-perl",
		"ci-php",
		"ci-plain",
		"ci-pypy",
		"ci-python",
		"ci-ruby",
		"ci-rust",
		"ci-scala",
		"ci-sqlite",
		"ci-swift",
		"ci-typescript",
		"ci-uhmlang",
		"ci-wasm",
		"ci-whitespace",
	}) {
		t.Fatalf("ci image names = %v", names)
	}
}
