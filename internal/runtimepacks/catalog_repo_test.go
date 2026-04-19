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
	if len(production) != 2 {
		t.Fatalf("expected 2 production images, got %d", len(production))
	}

	if production[0].Name != "type-a" || !reflect.DeepEqual(production[0].Languages, []string{"plain", "python"}) {
		t.Fatalf("type-a production image = %+v", production[0])
	}
	if production[1].Name != "type-b" || !reflect.DeepEqual(production[1].Languages, []string{"java"}) {
		t.Fatalf("type-b production image = %+v", production[1])
	}

	ci, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}
	names := make([]string, 0, len(ci))
	for _, spec := range ci {
		names = append(names, spec.Name)
	}
	if !reflect.DeepEqual(names, []string{"ci-java", "ci-plain", "ci-python"}) {
		t.Fatalf("ci image names = %v", names)
	}
}
