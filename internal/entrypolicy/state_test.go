package entrypolicy

import (
	"strings"
	"testing"
)

func TestStateExpectedRejectsInvalidOrEmptyPredicates(t *testing.T) {
	for _, source := range []string{
		`{}`, `{"version":1}`, `{"version":1,"objects":{}}`, `{"version":1,"objects":{"s":{}}}`,
		`{"version":1,"objects":{"s":{"unknown":1}}}`, `{"version":1,"variables":{"v":null}}`,
		`{"version":1,"variables":{"v":{}},"objects":{"s":{"x":0}}}`,
		`{"version":1,"variables":{"v":0},"tolerance":-1}`, `{"version":1,"variables":{"v":0},"tolerance":1.1}`,
		`{"version":1,"variables":{"v":0,"v":1}}`, `{"version":1,"variables":{"constructor":0}}`,
		`{"version":1,"variables":{"v":0}} {}`, `{"version":1,"objects":{"s":{"x":1e16}}}`,
		strings.Repeat("[", 65) + strings.Repeat("]", 65),
	} {
		if err := ValidateExpected([]byte(source)); err == nil {
			t.Fatalf("accepted invalid predicate %s", source)
		}
	}
}

func TestStateComparisonPartialTypedAndTolerant(t *testing.T) {
	actual := []byte(`{"version":1,"objects":{"s":{"x":30.0001,"visible":true,"say":"ok"}},"variables":{"v":"01","tolerance":2},"lists":{"l":["01",2],"empty":[]}}`)
	for _, tc := range []struct {
		expected string
		matches  bool
	}{
		{`{"version":1,"tolerance":0.001,"objects":{"s":{"x":30,"visible":true}}}`, true},
		{`{"version":1,"objects":{"s":{"x":30}}}`, false},
		{`{"version":1,"objects":{"missing":{"x":30}}}`, false},
		{`{"version":1,"variables":{"v":"01"}}`, true},
		{`{"version":1,"variables":{"v":1}}`, false},
		{`{"version":1,"variables":{"tolerance":1}}`, false},
		{`{"version":1,"lists":{"l":["01",2],"empty":[]}}`, true},
		{`{"version":1,"lists":{"l":["01"]}}`, false},
		{`{"version":1,"lists":{"empty":[]}}`, true},
	} {
		match, err := CompareState([]byte(tc.expected), actual)
		if err != nil || match != tc.matches {
			t.Fatalf("CompareState(%s) = %v, %v", tc.expected, match, err)
		}
	}
	if match, err := CompareState([]byte(`{"version":1}`), actual); err == nil || match {
		t.Fatal("empty expected state was accepted")
	}
	if match, err := CompareState([]byte(`{"version":1,"variables":{"v":0}}`), []byte(`invalid private output`)); err != nil || match {
		t.Fatal("malformed actual state should be a mismatch")
	}
	for _, tc := range []struct {
		tolerance string
		matches   bool
	}{
		{"", true},
		{`,"tolerance":0`, false},
	} {
		expected := []byte(`{"version":1,"objects":{"s":{"x":0}}` + tc.tolerance + `}`)
		actual := []byte(`{"version":1,"objects":{"s":{"x":6.123233995736766e-16}}}`)
		if match, err := CompareState(expected, actual); err != nil || match != tc.matches {
			t.Fatalf("default/explicit zero tolerance = %v, %v", match, err)
		}
	}
}
