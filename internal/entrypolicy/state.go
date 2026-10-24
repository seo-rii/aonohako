// Package entrypolicy defines the bounded, versioned Entry judging contract.
// Expected state is interpreted here in the host, never in the contestant runner.
package entrypolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"unicode/utf8"
)

const (
	Capability         = "entry-v1"
	SourceName         = "Main.entry.json"
	RunnerPath         = "/usr/local/lib/aonohako/entry-runner.mjs"
	MaxSubmissionBytes = 614400
	MaxInputBytes      = 131072
	MaxStateBytes      = 1048576
)

var errState = errors.New("invalid Entry state expectation")

// decodeBounded rejects duplicate and prototype-like keys as well as excessive
// JSON depth/nodes before any comparison. Errors never include private values.
func decodeBounded(data []byte, maximum int) (map[string]any, error) {
	if len(data) > maximum || !utf8.Valid(data) {
		return nil, errState
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	count := 0
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		count++
		if depth > 64 || count > 30000 {
			return nil, errState
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, errState
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return token, nil
		}
		switch delim {
		case '{':
			value := map[string]any{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || unsafeKey(key) {
					return nil, errState
				}
				if _, found := value[key]; found {
					return nil, errState
				}
				item, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				value[key] = item
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errState
			}
			return value, nil
		case '[':
			value := []any{}
			for decoder.More() {
				item, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				value = append(value, item)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errState
			}
			return value, nil
		default:
			return nil, errState
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errState
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errState
	}
	return object, nil
}

func unsafeKey(value string) bool {
	return value == "__proto__" || value == "constructor" || value == "prototype"
}
func validID(value string) bool {
	return len(value) > 0 && utf8.RuneCountInString(value) <= 128 && !unsafeKey(value)
}
func finite(value any) bool {
	n, ok := value.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= 1e15
}
func scalar(value any) bool {
	switch v := value.(type) {
	case bool:
		return true
	case string:
		return utf8.RuneCountInString(v) <= 65536
	default:
		return finite(value)
	}
}

func validateState(state map[string]any, expected bool) (float64, error) {
	if state["version"] != float64(1) {
		return 0, errState
	}
	tolerance := 0.000001
	if value, present := state["tolerance"]; present {
		if !expected || !finite(value) || value.(float64) < 0 || value.(float64) > 1 {
			return 0, errState
		}
		tolerance = value.(float64)
	}
	leaves, listItems := 0, 0
	for kind, raw := range state {
		if kind == "version" || kind == "tolerance" {
			continue
		}
		if kind != "objects" && kind != "variables" && kind != "lists" {
			return 0, errState
		}
		values, ok := raw.(map[string]any)
		if !ok || len(values) > 1000 {
			return 0, errState
		}
		if kind == "objects" && len(values) > 100 {
			return 0, errState
		}
		for id, value := range values {
			if !validID(id) {
				return 0, errState
			}
			switch kind {
			case "objects":
				fields, ok := value.(map[string]any)
				if !ok || len(fields) == 0 {
					return 0, errState
				}
				for key, value := range fields {
					switch key {
					case "x", "y", "rotation", "direction", "size":
						if !finite(value) {
							return 0, errState
						}
					case "visible":
						if _, ok := value.(bool); !ok {
							return 0, errState
						}
					case "say":
						if _, ok := value.(string); !ok || !scalar(value) {
							return 0, errState
						}
					default:
						return 0, errState
					}
					leaves++
				}
			case "variables":
				if !scalar(value) {
					return 0, errState
				}
				leaves++
			case "lists":
				items, ok := value.([]any)
				if !ok {
					return 0, errState
				}
				listItems += len(items)
				if listItems > 10000 {
					return 0, errState
				}
				for _, item := range items {
					if !scalar(item) {
						return 0, errState
					}
				}
				leaves++ // An empty list still asserts exact list emptiness.
			}
		}
	}
	if leaves == 0 {
		return 0, errState
	}
	return tolerance, nil
}

func ValidateExpected(data []byte) error {
	state, err := decodeBounded(data, MaxInputBytes)
	if err != nil {
		return err
	}
	_, err = validateState(state, true)
	return err
}

// CompareState performs a strict typed partial match. Every requested leaf must
// be present; arrays match in full, numbers use an absolute tolerance (0..1).
func CompareState(expected, actual []byte) (bool, error) {
	want, err := decodeBounded(expected, MaxInputBytes)
	if err != nil {
		return false, err
	}
	tolerance, err := validateState(want, true)
	if err != nil {
		return false, err
	}
	delete(want, "tolerance")
	got, err := decodeBounded(actual, MaxStateBytes)
	if err != nil {
		return false, nil
	}
	if _, err := validateState(got, false); err != nil {
		return false, nil
	}
	var matches func(any, any) bool
	matches = func(want, got any) bool {
		switch want := want.(type) {
		case map[string]any:
			values, ok := got.(map[string]any)
			if !ok {
				return false
			}
			for key, value := range want {
				actual, present := values[key]
				if !present || !matches(value, actual) {
					return false
				}
			}
			return true
		case []any:
			values, ok := got.([]any)
			if !ok || len(want) != len(values) {
				return false
			}
			for index, value := range want {
				if !matches(value, values[index]) {
					return false
				}
			}
			return true
		case float64:
			value, ok := got.(float64)
			return ok && math.Abs(want-value) <= tolerance
		default:
			return want == got
		}
	}
	return matches(want, got), nil
}
