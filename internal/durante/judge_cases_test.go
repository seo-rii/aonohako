package durante

import (
	"strings"
	"testing"
)

const judgeCaseInput = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const judgeCaseBinding = "work-clock-v1:sha256:" + judgeCaseInput
const judgeCaseEntry = `{"problem_id":"contest-1/a","source_language":"CPP20","stdin_sha256":"` + judgeCaseInput + `","time_ms":50,"model_binding":"` + judgeCaseBinding + `"}`

func TestJudgeCasesRequireExactKeyAndModel(t *testing.T) {
	registry, err := ParseJudgeCases(`{"schema":1,"cases":[` + judgeCaseEntry + `,` + strings.Replace(judgeCaseEntry, "50", "51", 1) + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Allows("contest-1/a", "CPP20", judgeCaseInput, 50, 0, 64, judgeCaseBinding) || !registry.Allows("contest-1/a", "CPP20", judgeCaseInput, 51, 200, 32, judgeCaseBinding) {
		t.Fatal("registered case and deadlines did not match")
	}
	for _, tc := range []struct {
		problemID, language, stdin, binding string
		deadline                            int
	}{
		{"contest-1/b", "CPP20", judgeCaseInput, judgeCaseBinding, 50},
		{"contest-1/a", "cpp20", judgeCaseInput, judgeCaseBinding, 50},
		{"contest-1/a", "CPP20", strings.Repeat("a", 64), judgeCaseBinding, 50},
		{"contest-1/a", "CPP20", judgeCaseInput, "", 50},
		{"contest-1/a", "CPP20", judgeCaseInput, judgeCaseBinding, 49},
	} {
		if registry.Allows(tc.problemID, tc.language, tc.stdin, tc.deadline, 100, 64, tc.binding) {
			t.Errorf("matched unregistered case %+v", tc)
		}
	}
}

func TestJudgeCasesRejectMalformedOrAmbiguousScope(t *testing.T) {
	wrap := func(cases string) string { return `{"schema":1,"cases":[` + cases + `]}` }
	valid := wrap(judgeCaseEntry)
	for name, raw := range map[string]string{
		"empty setting":        "",
		"whitespace":           "  ",
		"invalid UTF-8":        strings.Replace(valid, "contest-1/a", "\xff", 1),
		"oversized input":      valid + strings.Repeat(" ", maxJudgeCasesBytes),
		"oversized list":       wrap(strings.Repeat("{},", maxJudgeCases) + "{}"),
		"empty list":           wrap(""),
		"unsupported schema":   strings.Replace(valid, `"schema":1`, `"schema":3`, 1),
		"duplicate root field": strings.Replace(valid, `"schema":1`, `"schema":1,"schema":1`, 1),
		"unknown root field":   strings.Replace(valid, `"schema":1`, `"schema":1,"extra":true`, 1),
		"trailing data":        valid + "{}",
		"missing required key": strings.Replace(valid, `"time_ms":50,`, "", 1),
		"duplicate case field": strings.Replace(valid, `"time_ms":50`, `"time_ms":50,"time_ms":50`, 1),
		"case-folded field":    strings.Replace(valid, "time_ms", "Time_MS", 1),
		"null deadline":        strings.Replace(valid, ":50", ":null", 1),
		"fractional deadline":  strings.Replace(valid, ":50", ":1.5", 1),
		"zero deadline":        strings.Replace(valid, ":50", ":0", 1),
		"empty problem":        strings.Replace(valid, "contest-1/a", "", 1),
		"invalid problem":      strings.Replace(valid, "contest-1/a", "../contest", 1),
		"language alias":       strings.Replace(valid, "CPP20", "cpp20", 1),
		"uppercase digest":     strings.Replace(valid, judgeCaseInput, strings.ToUpper(judgeCaseInput), 1),
		"short digest":         strings.Replace(valid, judgeCaseInput, "abcd", 1),
		"bare binding":         strings.Replace(valid, judgeCaseBinding, judgeCaseInput, 1),
		"uppercase binding":    strings.Replace(valid, judgeCaseBinding, "work-clock-v1:sha256:"+strings.ToUpper(judgeCaseInput), 1),
		"duplicate case":       wrap(judgeCaseEntry + "," + judgeCaseEntry),
		"conflicting models":   wrap(judgeCaseEntry + "," + strings.Replace(judgeCaseEntry, judgeCaseBinding, "work-clock-v1:sha256:"+strings.Repeat("0", 64), 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseJudgeCases(raw); err == nil || got != nil {
				t.Fatalf("invalid registry accepted: %+v %v", got, err)
			}
		})
	}
}

func TestJudgeCasesMatchNominalLimits(t *testing.T) {
	entry := strings.Replace(judgeCaseEntry, `"time_ms":50`, `"time_ms":50,"wall_time_ms":200,"memory_mb":64`, 1)
	other := strings.Replace(entry, `"wall_time_ms":200`, `"wall_time_ms":0`, 1)
	reg, err := ParseJudgeCases(`{"schema":2,"cases":[` + entry + "," + other + "]}")
	if err != nil {
		t.Fatal(err)
	}
	if !reg.BindsNominalLimits() || !reg.Allows("contest-1/a", "CPP20", judgeCaseInput, 50, 200, 64, judgeCaseBinding) ||
		!reg.Allows("contest-1/a", "CPP20", judgeCaseInput, 50, 0, 64, judgeCaseBinding) ||
		reg.Allows("contest-1/a", "CPP20", judgeCaseInput, 50, 201, 64, judgeCaseBinding) ||
		reg.Allows("contest-1/a", "CPP20", judgeCaseInput, 50, 200, 63, judgeCaseBinding) {
		t.Fatal("case lookup did not distinguish nominal resource limits")
	}
	for _, invalid := range []string{
		judgeCaseEntry, strings.Replace(entry, `"memory_mb":64,`, "", 1),
		strings.Replace(entry, `"memory_mb":64`, `"memory_mb":0`, 1),
		strings.Replace(entry, `"wall_time_ms":200`, `"wall_time_ms":-1`, 1),
		strings.Replace(entry, `"wall_time_ms":200`, `"wall_time_ms":null`, 1),
		strings.Replace(entry, `"wall_time_ms":200`, `"wall_time_ms":600001`, 1),
		strings.Replace(entry, `"memory_mb":64`, `"memory_mb":4097`, 1),
	} {
		if _, err := ParseJudgeCases(`{"schema":2,"cases":[` + invalid + "]}"); err == nil {
			t.Fatalf("accepted missing or invalid nominal limits: %s", invalid)
		}
	}
}
