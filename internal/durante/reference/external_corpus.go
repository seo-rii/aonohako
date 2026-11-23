package reference

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	validationManifestBytes = 16 << 20
	validationMaxWorkloads  = 64
	validationMaxCases      = 128
	validationSourceBytes   = 1 << 20
	validationInputBytes    = 1 << 20
	validationOracleBytes   = 64 << 10
)

var validationWorkloadID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidationCorpus retains the held-out workloads and digest of the exact manifest bytes.
type ValidationCorpus struct {
	digest    string
	workloads []Workload
}

func (c *ValidationCorpus) SHA256() string {
	if c == nil {
		return ""
	}
	return c.digest
}

func (c *ValidationCorpus) snapshot() ([]Workload, error) {
	if c == nil || len(c.workloads) == 0 {
		return nil, fmt.Errorf("validation corpus has no loaded manifest")
	}
	return c.workloads, nil
}

func referenceCollectionProfile(dataset Dataset) (CorpusProfile, error) {
	if dataset.CorpusID != ExternalCorpusID {
		return referenceDatasetProfile(dataset)
	}
	if dataset.SourceLanguage != "CPP20" || dataset.Smoke || !referenceSHA256(dataset.CorpusSHA256) || !referenceSHA256(dataset.FrozenPolicySHA256) {
		return CorpusProfile{}, fmt.Errorf("invalid external validation corpus identity")
	}
	return ResolveCorpusProfile(dataset.SourceLanguage)
}

func LoadValidationCorpus(path string) (*ValidationCorpus, error) {
	data, err := collectionReadFile(path, validationManifestBytes)
	if err != nil {
		return nil, err
	}
	return decodeValidationCorpus(data)
}

func decodeValidationCorpus(data []byte) (*ValidationCorpus, error) {
	if !utf8.Valid(data) || !json.Valid(data) || !validManifestEscapes(data) {
		return nil, fmt.Errorf("validation corpus requires a UTF-8 JSON document")
	}
	var schema int
	var language string
	var workloads json.RawMessage
	if err := manifestFields(data, map[string]any{"schema": &schema, "language": &language, "workloads": &workloads}); err != nil {
		return nil, err
	}
	if schema != 1 || language != "CPP20" {
		return nil, fmt.Errorf("validation corpus requires schema 1 and language CPP20")
	}
	result := &ValidationCorpus{digest: collectionSHA(data)}
	ids := make(map[string]bool)
	totalCases := 0
	err := manifestArray(workloads, validationMaxWorkloads, func(raw json.RawMessage) error {
		w := Workload{Split: SplitValidation}
		var cases json.RawMessage
		if err := manifestFields(raw, map[string]any{"id": &w.ID, "source": &w.Source, "cases": &cases}); err != nil {
			return err
		}
		if !validationWorkloadID.MatchString(w.ID) || ids[w.ID] {
			return fmt.Errorf("invalid or duplicate validation workload ID")
		}
		if strings.TrimSpace(w.Source) == "" || len(w.Source) > validationSourceBytes {
			return fmt.Errorf("validation workload %s has empty or oversized source", w.ID)
		}
		ids[w.ID] = true
		scales := make(map[int]bool)
		err := manifestArray(cases, validationMaxCases-totalCases, func(raw json.RawMessage) error {
			var c Case
			if err := manifestFields(raw, map[string]any{"scale": &c.Scale, "input": &c.Input, "expected_stdout": &c.ExpectedStdout}); err != nil {
				return err
			}
			if c.Scale <= 0 || scales[c.Scale] || len(c.Input) > validationInputBytes || len(c.ExpectedStdout) > validationOracleBytes || strings.TrimSpace(c.ExpectedStdout) == "" {
				return fmt.Errorf("invalid scale, input or expected stdout for validation workload %s", w.ID)
			}
			scales[c.Scale] = true
			w.Cases = append(w.Cases, c)
			return nil
		})
		if err != nil {
			return err
		}
		totalCases += len(w.Cases)
		result.workloads = append(result.workloads, w)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD.
// Reject those escapes so the submitted source and data retain their exact text.
// The caller has checked JSON syntax, including escape length and hex digits.
func validManifestEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		first, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if !utf16.IsSurrogate(rune(first)) {
			continue
		}
		if i+6 >= len(data) || !bytes.HasPrefix(data[i+1:], []byte(`\u`)) {
			return false
		}
		second, _ := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if utf16.DecodeRune(rune(first), rune(second)) == utf8.RuneError {
			return false
		}
		i += 6
	}
	return true
}

func manifestFields(raw []byte, fields map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("expected a validation manifest object")
	}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		dst := fields[key]
		if err != nil || !ok || dst == nil {
			return fmt.Errorf("unknown or duplicate validation manifest field %q", key)
		}
		delete(fields, key)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(value, []byte("null")) {
			return fmt.Errorf("null validation manifest field %q", key)
		}
		if err := json.Unmarshal(value, dst); err != nil {
			return fmt.Errorf("invalid validation manifest field %q: %w", key, err)
		}
	}
	if len(fields) != 0 {
		return fmt.Errorf("missing required validation manifest fields")
	}
	return nil
}

func manifestArray(raw []byte, limit int, visit func(json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') || !dec.More() {
		return fmt.Errorf("expected a nonempty validation manifest array")
	}
	for i := 0; dec.More(); i++ {
		if i >= limit {
			return fmt.Errorf("validation corpus exceeds workload or case count limit")
		}
		var entry json.RawMessage
		if err := dec.Decode(&entry); err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	return nil
}
