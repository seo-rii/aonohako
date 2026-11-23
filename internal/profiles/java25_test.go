package profiles

import "testing"

func TestJava25ProfileUsesDedicatedRuntimeAndExistingJavaLimits(t *testing.T) {
	java25, ok := Resolve("JAVA25")
	if !ok {
		t.Fatal("JAVA25 is unsupported")
	}
	if java25.SourceLang != "JAVA25" || java25.CompileKind != "java" || java25.JavaRelease != "25" || java25.RunLang != "java25" || java25.Extension != "java" {
		t.Fatalf("JAVA25 profile = %+v", java25)
	}
	java21, ok := Resolve("JAVA21")
	if !ok {
		t.Fatal("JAVA21 is unsupported")
	}
	java25.SourceLang = java21.SourceLang
	java25.JavaRelease = java21.JavaRelease
	java25.RunLang = java21.RunLang
	if java25 != java21 {
		t.Fatalf("Java 25 resource defaults differ from Java 21: %+v versus %+v", java25, java21)
	}
	legacy, ok := Resolve("JAVA")
	if !ok || legacy.JavaRelease != "11" || legacy.RunLang != "java" {
		t.Fatalf("legacy Java defaults changed: %+v", legacy)
	}
}

func TestNormalizeJava25RunLanguage(t *testing.T) {
	for _, input := range []string{"JAVA25", "java25", "Java25", " java25 "} {
		if got := NormalizeRunLang(input); got != "java25" {
			t.Errorf("NormalizeRunLang(%q) = %q, want java25", input, got)
		}
	}
}
