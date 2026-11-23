package durante

import (
	"aonohako/internal/cpupolicy"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxProcesses  = 256
	maxFileBytes  = 1 << 20
	maxTotalBytes = 16 << 20
	maxEvents     = 4096
	maxLineBytes  = 3072
)

var reportName = regexp.MustCompile(`^process-([1-9][0-9]*)\.jsonl$`)
var segmentSuffix = regexp.MustCompile(`^[0-9]+$`)

type event struct {
	Schema                int     `json:"schema"`
	ClientVersion         int     `json:"client_version"`
	Authority             string  `json:"authority"`
	Metric                string  `json:"metric"`
	PID                   int     `json:"pid"`
	Segment               string  `json:"segment"`
	Seq                   uint64  `json:"seq"`
	Event                 string  `json:"event"`
	SDKVersion            string  `json:"sdk_version"`
	Origin                string  `json:"origin"`
	ParentPID             int     `json:"parent_pid"`
	ParentSegment         string  `json:"parent_segment"`
	REPEnabled            bool    `json:"rep_bytes_enabled"`
	ChildPID              int     `json:"child_pid"`
	Syscall               int     `json:"syscall"`
	Supported             bool    `json:"supported"`
	Result                int64   `json:"result"`
	Instructions          uint64  `json:"instructions"`
	REPBytes              uint64  `json:"rep_bytes"`
	REPEntries            uint64  `json:"rep_entries"`
	UnsupportedREPEntries uint64  `json:"unsupported_rep_entries"`
	Signals               uint64  `json:"signals_observed"`
	Valid                 bool    `json:"valid"`
	AccountingError       bool    `json:"accounting_error"`
	ThreadsStarted        uint64  `json:"threads_started"`
	ThreadsFinished       uint64  `json:"threads_finished"`
	ThreadsComplete       bool    `json:"threads_complete"`
	Quality               string  `json:"quality"`
	ExitSeen              bool    `json:"app_exit_seen"`
	ExitCode              uint64  `json:"app_exit_code"`
	REPBreakdownVersion   *uint64 `json:"rep_breakdown_version,omitempty"`
	REPMOVSBytes          *uint64 `json:"rep_movs_bytes,omitempty"`
	REPSTOSBytes          *uint64 `json:"rep_stos_bytes,omitempty"`
	REPMOVSEntries        *uint64 `json:"rep_movs_entries,omitempty"`
	REPSTOSEntries        *uint64 `json:"rep_stos_entries,omitempty"`
	CPUPolicyVersion      *uint64 `json:"cpu_policy_version,omitempty"`
	CPUPolicy             *string `json:"cpu_policy,omitempty"`
	CPUFeature            *string `json:"feature,omitempty"`
	CPUIDProfileVersion   *uint64 `json:"cpuid_profile_version,omitempty"`
	CPUIDProfile          *string `json:"cpuid_profile,omitempty"`
	LatencyBoundVersion   *uint64 `json:"latency_bound_version,omitempty"`
	LatencyBoundX16       *uint64 `json:"latency_bound_x16,omitempty"`

	MemoryOperandBytesVersion *uint64 `json:"memory_operand_bytes_version,omitempty"`
	MemoryOperandBytes        *uint64 `json:"memory_operand_bytes,omitempty"`
}

func decodeEvent(data []byte) (event, error) {
	var row event
	if !utf8.Valid(data) {
		return row, errors.New("invalid report UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return row, errors.New("invalid report object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return row, errors.New("invalid report key")
		}
		if _, exists := fields[key]; exists {
			return row, errors.New("duplicate report key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return row, errors.New("invalid report value")
		}
		// Schema 1 fields are scalars. Reject nested structures instead of
		// allowing unknown objects to conceal duplicate keys.
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) != 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return row, errors.New("non-scalar report field")
		}
		fields[key] = raw
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return row, errors.New("invalid report object end")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return row, errors.New("trailing report data")
	}
	// Shadow the new fields while decoding the old record. Their exact keys
	// are decoded below; encoding/json's case-folded aliases must not advertise
	// or populate detail that the upstream scalar-key collectors would ignore.
	legacy := struct {
		*event
		Schema              json.RawMessage `json:"schema"`
		ClientVersion       json.RawMessage `json:"client_version"`
		Authority           json.RawMessage `json:"authority"`
		Metric              json.RawMessage `json:"metric"`
		PID                 json.RawMessage `json:"pid"`
		Segment             json.RawMessage `json:"segment"`
		Seq                 json.RawMessage `json:"seq"`
		Event               json.RawMessage `json:"event"`
		SDKVersion          json.RawMessage `json:"sdk_version"`
		Origin              json.RawMessage `json:"origin"`
		ParentPID           json.RawMessage `json:"parent_pid"`
		ParentSegment       json.RawMessage `json:"parent_segment"`
		REPEnabled          json.RawMessage `json:"rep_bytes_enabled"`
		REPBreakdownVersion json.RawMessage `json:"rep_breakdown_version"`
		REPMOVSBytes        json.RawMessage `json:"rep_movs_bytes"`
		REPSTOSBytes        json.RawMessage `json:"rep_stos_bytes"`
		REPMOVSEntries      json.RawMessage `json:"rep_movs_entries"`
		REPSTOSEntries      json.RawMessage `json:"rep_stos_entries"`
		CPUPolicyVersion    json.RawMessage `json:"cpu_policy_version"`
		CPUPolicy           json.RawMessage `json:"cpu_policy"`
		CPUFeature          json.RawMessage `json:"feature"`
		CPUIDProfileVersion json.RawMessage `json:"cpuid_profile_version"`
		CPUIDProfile        json.RawMessage `json:"cpuid_profile"`
		Signals             json.RawMessage `json:"signals_observed"`
		Quality             json.RawMessage `json:"quality"`
		Instructions        json.RawMessage `json:"instructions"`
		LatencyBoundVersion json.RawMessage `json:"latency_bound_version"`
		LatencyBoundX16     json.RawMessage `json:"latency_bound_x16"`

		MemoryOperandBytesVersion json.RawMessage `json:"memory_operand_bytes_version"`
		MemoryOperandBytes        json.RawMessage `json:"memory_operand_bytes"`
	}{event: &row}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return row, errors.New("invalid report field type")
	}
	// A native startup receipt relies on these exact metadata keys. Decode
	// them explicitly so an unknown case-folded alias cannot replace a root's
	// schema, identity, origin, or lifecycle sequence.
	for key, target := range map[string]any{
		"schema": &row.Schema, "client_version": &row.ClientVersion,
		"authority": &row.Authority, "metric": &row.Metric, "pid": &row.PID,
		"segment": &row.Segment, "seq": &row.Seq, "event": &row.Event,
		"sdk_version": &row.SDKVersion, "origin": &row.Origin,
		"parent_pid": &row.ParentPID, "parent_segment": &row.ParentSegment,
		"rep_bytes_enabled": &row.REPEnabled,
		"instructions":      &row.Instructions,
		"signals_observed":  &row.Signals, "quality": &row.Quality,
	} {
		if raw, present := fields[key]; present && json.Unmarshal(raw, target) != nil {
			return row, errors.New("invalid report field type")
		}
	}
	for key, target := range map[string]**uint64{
		"rep_breakdown_version": &row.REPBreakdownVersion,
		"rep_movs_bytes":        &row.REPMOVSBytes, "rep_stos_bytes": &row.REPSTOSBytes,
		"rep_movs_entries": &row.REPMOVSEntries, "rep_stos_entries": &row.REPSTOSEntries,
		"cpu_policy_version":           &row.CPUPolicyVersion,
		"cpuid_profile_version":        &row.CPUIDProfileVersion,
		"latency_bound_version":        &row.LatencyBoundVersion,
		"latency_bound_x16":            &row.LatencyBoundX16,
		"memory_operand_bytes_version": &row.MemoryOperandBytesVersion,
		"memory_operand_bytes":         &row.MemoryOperandBytes,
	} {
		if raw, present := fields[key]; present {
			var value uint64
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
				return row, errors.New("invalid unsigned report field: " + key)
			}
			*target = &value
		}
	}
	if row.REPBreakdownVersion != nil && *row.REPBreakdownVersion != 1 {
		return row, errors.New("unsupported REP breakdown version")
	}
	if row.LatencyBoundVersion != nil && (row.Event != "start" || *row.LatencyBoundVersion != 1) {
		return row, errors.New("invalid latency bound capability")
	}
	if row.LatencyBoundX16 != nil && row.Event != "end" && row.Event != "exec_attempt" {
		return row, errors.New("latency bound outside terminal")
	}
	if row.MemoryOperandBytesVersion != nil && (row.Event != "start" || *row.MemoryOperandBytesVersion != 1 || row.LatencyBoundVersion == nil) {
		return row, errors.New("invalid memory operand bytes capability")
	}
	if row.MemoryOperandBytes != nil && row.Event != "end" && row.Event != "exec_attempt" {
		return row, errors.New("memory operand bytes outside terminal")
	}
	for key, target := range map[string]**string{"cpu_policy": &row.CPUPolicy, "feature": &row.CPUFeature, "cpuid_profile": &row.CPUIDProfile} {
		if raw, present := fields[key]; present {
			var value string
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
				return row, errors.New("invalid CPU policy field type")
			}
			*target = &value
		}
	}
	if _, err := eventCPUPolicy(row); err != nil {
		return row, err
	}
	if _, err := eventCPUIDProfile(row); err != nil {
		return row, err
	}
	required := []string{"schema", "client_version", "authority", "metric", "pid", "segment", "seq", "event"}
	switch row.Event {
	case "start":
		required = append(required, "sdk_version", "origin", "parent_pid", "parent_segment", "rep_bytes_enabled")
	case "spawn":
		required = append(required, "child_pid", "syscall", "supported")
	case "exec_failed":
		required = append(required, "syscall", "result")
	case "cpu_feature_violation":
		required = append(required, "feature")
	case "exec_attempt", "end":
		required = append(required, "instructions", "rep_bytes", "rep_entries", "unsupported_rep_entries", "signals_observed", "valid", "accounting_error", "threads_started", "threads_finished", "threads_complete", "quality", "app_exit_code")
	default:
		return row, errors.New("unsupported lifecycle event")
	}
	if row.Event == "end" {
		required = append(required, "app_exit_seen")
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return row, errors.New("missing report field")
		}
	}
	return row, nil
}

func validPID(pid int) bool { return pid > 0 && int64(pid) <= math.MaxInt32 }

func eventCPUPolicy(row event) (string, error) {
	if row.CPUFeature != nil && row.Event != "cpu_feature_violation" {
		return "", errors.New("CPU feature outside violation")
	}
	if row.CPUPolicyVersion == nil && row.CPUPolicy == nil {
		return "", nil
	}
	if row.Event != "start" || row.CPUPolicyVersion == nil || row.CPUPolicy == nil || *row.CPUPolicyVersion != 1 {
		return "", errors.New("invalid CPU policy capability")
	}
	switch *row.CPUPolicy {
	case "avx", "simd", "avx,simd":
		return *row.CPUPolicy, nil
	default:
		return "", errors.New("invalid CPU policy")
	}
}

// CPUID profiles reported by the client. Absent means host CPUID was visible.
const (
	CPUIDProfileX8664V1 = "x86-64-v1"
	CPUIDProfileX8664V2 = "x86-64-v2"
)

// ValidCPUIDProfile accepts only profiles the vendored client implements.
func ValidCPUIDProfile(profile string) bool {
	return profile == CPUIDProfileX8664V1 || profile == CPUIDProfileX8664V2
}

func eventCPUIDProfile(row event) (string, error) {
	if row.CPUIDProfileVersion == nil && row.CPUIDProfile == nil {
		return "", nil
	}
	if row.Event != "start" || row.CPUIDProfileVersion == nil || row.CPUIDProfile == nil || *row.CPUIDProfileVersion != 1 {
		return "", errors.New("invalid CPUID profile capability")
	}
	if !ValidCPUIDProfile(*row.CPUIDProfile) {
		return "", errors.New("invalid CPUID profile")
	}
	return *row.CPUIDProfile, nil
}

type lineage struct {
	parent  int
	segment string
}

func terminalCounters(row event, isExec, repEnabled bool) ([5]uint64, error) {
	counts := [5]uint64{row.Instructions, row.REPBytes, row.REPEntries, row.UnsupportedREPEntries, row.Signals}
	if !row.Valid || row.AccountingError || row.ThreadsStarted == 0 || row.ThreadsStarted != row.ThreadsFinished || !row.ThreadsComplete || row.Instructions == 0 {
		return counts, errors.New("incomplete terminal accounting")
	}
	expectedQuality := "uninterrupted"
	if row.Signals != 0 {
		expectedQuality = "signals_observed"
	}
	if row.Quality != expectedQuality {
		return counts, errors.New("signal quality mismatch")
	}
	if !repEnabled && (row.REPBytes != 0 || row.REPEntries != 0 || row.UnsupportedREPEntries != 0) {
		return counts, errors.New("REP counters without REP mode")
	}
	if row.REPBytes != 0 && row.REPEntries == 0 {
		return counts, errors.New("REP bytes without REP entries")
	}
	if row.ExitCode > 255 || (!isExec && !row.ExitSeen) {
		return counts, errors.New("application exit unavailable")
	}
	return counts, nil
}

func terminalREPBreakdown(row event, enabled, advertised bool) (*REPBreakdown, error) {
	fields := [4]*uint64{row.REPMOVSBytes, row.REPSTOSBytes, row.REPMOVSEntries, row.REPSTOSEntries}
	for _, field := range fields {
		if (field != nil) != advertised {
			return nil, errors.New("REP breakdown fields do not match advertised capability")
		}
	}
	if !advertised {
		return nil, nil
	}
	breakdown := REPBreakdown{Version: 1, MOVSBytes: *fields[0], STOSBytes: *fields[1], MOVSEntries: *fields[2], STOSEntries: *fields[3]}
	if !breakdown.validFor(row.REPBytes, row.REPEntries, enabled) {
		return nil, errors.New("REP breakdown partition mismatch")
	}
	return &breakdown, nil
}

func nativeCPUStartupPolicy(streams map[int][]event, rootPID int) ([]cpupolicy.Feature, string, error) {
	if !validPID(rootPID) || len(streams) == 0 || len(streams) > maxProcesses {
		return nil, "", errors.New("root PID or process count invalid")
	}
	rootRows := streams[rootPID]
	if len(rootRows) == 0 {
		return nil, "", errors.New("root process report missing")
	}
	root := rootRows[0]
	prefix := strconv.Itoa(rootPID) + "-"
	if root.Event != "start" || root.Schema != 1 || root.ClientVersion != 1 || root.Metric != Metric || root.Authority != Authority ||
		root.PID != rootPID || root.Seq != 1 || root.SDKVersion != "11.3.0" ||
		!strings.HasPrefix(root.Segment, prefix) || !segmentSuffix.MatchString(strings.TrimPrefix(root.Segment, prefix)) ||
		root.Origin != "initial" || root.ParentPID != 0 || root.ParentSegment != "" {
		return nil, "", errors.New("invalid root start metadata")
	}
	// Inspect every start before a violation can return early. An incomplete
	// violating image must not hide mismatched policies in another process.
	// Every image must also observe the same virtual processor: a host-visible
	// image would reintroduce processor-dependent dispatch into the total.
	policy, policyKnown := "", false
	cpuid, cpuidKnown := "", false
	for _, rows := range streams {
		for _, row := range rows {
			current, err := eventCPUPolicy(row)
			if err != nil {
				return nil, "", err
			}
			profile, err := eventCPUIDProfile(row)
			if err != nil {
				return nil, "", err
			}
			if row.Event == "start" {
				if policyKnown && current != policy {
					return nil, "", errors.New("mixed CPU policies")
				}
				policy, policyKnown = current, true
				if cpuidKnown && profile != cpuid {
					return nil, "", errors.New("mixed CPUID profiles")
				}
				cpuid, cpuidKnown = profile, true
			}
		}
	}
	var features []cpupolicy.Feature
	if policy != "" {
		for _, feature := range strings.Split(policy, ",") {
			features = append(features, cpupolicy.Feature(feature))
		}
	}
	return features, cpuid, nil
}

func aggregate(streams map[int][]event, rootPID int) (report Report, err error) {
	features, cpuid, err := nativeCPUStartupPolicy(streams, rootPID)
	if err != nil {
		return Report{}, err
	}
	// Keep only locally validated startup metadata when a later guest fault
	// makes totals unavailable. It never survives JSON serialization.
	defer func() { report.nativeCPUFeatures = append([]cpupolicy.Feature(nil), features...) }()
	policy := ""
	for _, feature := range features {
		if policy != "" {
			policy += ","
		}
		policy += string(feature)
	}
	segments := map[string]bool{}
	processes := map[int]lineage{}
	edges := map[int]lineage{}
	var totals [5]uint64
	breakdownTotals := REPBreakdown{Version: 1}
	var latency *LatencyBound
	if streams[rootPID][0].LatencyBoundVersion != nil {
		latency = &LatencyBound{Version: 1}
	}
	var operandBytes *MemoryOperandBytes
	if streams[rootPID][0].MemoryOperandBytesVersion != nil {
		operandBytes = &MemoryOperandBytes{Version: 1}
	}
	images := 0
	repKnown, repEnabled, breakdownEnabled := false, false, false
	sum := func(row event, isExec bool) error {
		counts, err := terminalCounters(row, isExec, repEnabled)
		if err != nil {
			return err
		}
		breakdown, err := terminalREPBreakdown(row, repEnabled, breakdownEnabled)
		if err != nil {
			return err
		}
		if breakdown != nil {
			breakdownTotals, err = breakdownTotals.checkedAdd(*breakdown)
			if err != nil {
				return err
			}
		}
		if (row.LatencyBoundX16 != nil) != (latency != nil) {
			return errors.New("latency bound capability mismatch")
		}
		if latency != nil {
			work := *row.LatencyBoundX16
			if row.Instructions > work/4 {
				return errors.New("latency bound below issue cost")
			}
			if work > math.MaxUint64-latency.CyclesX16 {
				return errors.New("latency bound aggregate overflow")
			}
			latency.CyclesX16 += work
		}
		if (row.MemoryOperandBytes != nil) != (operandBytes != nil) {
			return errors.New("memory operand bytes capability mismatch")
		}
		if operandBytes != nil {
			if row.Signals != 0 {
				return errors.New("memory operand bytes interrupted by signal")
			}
			if *row.MemoryOperandBytes > math.MaxUint64-operandBytes.Bytes {
				return errors.New("memory operand bytes aggregate overflow")
			}
			operandBytes.Bytes += *row.MemoryOperandBytes
		}
		for i, value := range counts {
			if totals[i] > math.MaxUint64-value {
				return errors.New("tree counter overflow")
			}
			totals[i] += value
		}
		images++
		return nil
	}
	for pid, rows := range streams {
		if !validPID(pid) || len(rows) == 0 || len(rows) > maxEvents {
			return Report{}, errors.New("invalid process stream")
		}
		live := ""
		ended := false
		sequence := uint64(0)
		var pending *event
		for i := range rows {
			row := rows[i]
			if row.Schema != 1 || row.ClientVersion != 1 || row.Metric != Metric || row.Authority != Authority || row.PID != pid {
				return Report{}, errors.New("record version or identity mismatch")
			}
			if row.REPBreakdownVersion != nil && *row.REPBreakdownVersion != 1 {
				return Report{}, errors.New("unsupported REP breakdown version")
			}
			prefix := strconv.Itoa(pid) + "-"
			if !strings.HasPrefix(row.Segment, prefix) || !segmentSuffix.MatchString(strings.TrimPrefix(row.Segment, prefix)) {
				return Report{}, errors.New("invalid image segment")
			}
			if row.Event == "start" {
				if ended || segments[row.Segment] || row.Seq != 1 || row.SDKVersion != "11.3.0" {
					return Report{}, errors.New("invalid start or reused PID")
				}
				if repKnown && repEnabled != row.REPEnabled {
					return Report{}, errors.New("mixed REP modes")
				}
				if repKnown && breakdownEnabled != (row.REPBreakdownVersion != nil) {
					return Report{}, errors.New("mixed REP breakdown capabilities")
				}
				if (row.LatencyBoundVersion != nil) != (latency != nil) {
					return Report{}, errors.New("mixed latency bound capabilities")
				}
				if (row.MemoryOperandBytesVersion != nil) != (operandBytes != nil) {
					return Report{}, errors.New("mixed memory operand bytes capabilities")
				}
				repKnown = true
				repEnabled = row.REPEnabled
				breakdownEnabled = row.REPBreakdownVersion != nil
				if live == "" {
					if pid == rootPID {
						if row.Origin != "initial" || row.ParentPID != 0 || row.ParentSegment != "" {
							return Report{}, errors.New("invalid root origin")
						}
					} else if row.Origin != "fork" || !validPID(row.ParentPID) {
						return Report{}, errors.New("unlinked child origin")
					}
					processes[pid] = lineage{row.ParentPID, row.ParentSegment}
				} else {
					if pending == nil || row.Origin != "initial" || row.ParentPID != 0 || row.ParentSegment != "" {
						return Report{}, errors.New("exec image without checkpoint")
					}
					if err := sum(*pending, true); err != nil {
						return Report{}, err
					}
				}
				live = row.Segment
				segments[live] = true
				sequence = 1
				pending = nil
				continue
			}
			sequence++
			if live == "" || ended || row.Segment != live || row.Seq != sequence {
				return Report{}, errors.New("event outside live image or sequence gap")
			}
			switch row.Event {
			case "cpu_feature_violation":
				if row.CPUFeature == nil || (*row.CPUFeature != "avx" && *row.CPUFeature != "simd") ||
					(policy != *row.CPUFeature && policy != "avx,simd") {
					return Report{}, errors.New("CPU violation outside requested policy")
				}
				return Report{}, errors.New("disallowed CPU feature: " + *row.CPUFeature)
			case "spawn":
				if !validPID(row.ChildPID) || row.ChildPID == pid || !row.Supported {
					return Report{}, errors.New("invalid child edge")
				}
				if _, exists := edges[row.ChildPID]; exists {
					return Report{}, errors.New("duplicate child edge")
				}
				if row.Syscall != 56 && row.Syscall != 57 && row.Syscall != 58 && row.Syscall != 435 {
					return Report{}, errors.New("invalid spawn syscall")
				}
				edges[row.ChildPID] = lineage{pid, live}
			case "exec_attempt":
				if pending != nil {
					return Report{}, errors.New("missing exec result")
				}
				copy := row
				pending = &copy
			case "exec_failed":
				if pending == nil || row.Result >= 0 || (row.Syscall != 59 && row.Syscall != 322) {
					return Report{}, errors.New("invalid failed exec")
				}
				pending = nil
			case "end":
				if pending != nil {
					return Report{}, errors.New("exec outcome missing")
				}
				if err := sum(row, false); err != nil {
					return Report{}, err
				}
				ended = true
			default:
				return Report{}, errors.New("unsupported lifecycle event")
			}
		}
		if !ended {
			return Report{}, errors.New("process has no completed final image")
		}
	}
	if len(edges) != len(processes)-1 {
		return Report{}, errors.New("missing or unannounced child process")
	}
	for child, parent := range edges {
		actual, ok := processes[child]
		if !ok || actual != parent {
			return Report{}, errors.New("child lineage mismatch")
		}
		if _, ok := processes[parent.parent]; !ok {
			return Report{}, errors.New("child parent missing")
		}
	}
	visited := map[int]bool{rootPID: true}
	for range processes {
		for child, parent := range edges {
			if visited[parent.parent] {
				visited[child] = true
			}
		}
	}
	if len(visited) != len(processes) {
		return Report{}, errors.New("disconnected process graph")
	}
	quality := "uninterrupted"
	if totals[4] != 0 {
		quality = "signals_observed"
	}
	var breakdown *REPBreakdown
	if breakdownEnabled {
		breakdown = &breakdownTotals
	}
	return Report{Schema: 1, ClientVersion: 1, Metric: Metric, Authority: Authority, Scope: Scope, REPEnabled: repEnabled, Complete: true, Quality: quality, Instructions: &totals[0], REPBytes: &totals[1], REPEntries: &totals[2], UnsupportedREPEntries: &totals[3], SignalsObserved: totals[4], Processes: len(processes), Images: images,
		IntegrityVerified: diagnosticFalse(), MeasurementContract: DiagnosticContract(), REPTelemetryStatus: repTelemetryStatus(repEnabled, totals[3]), REPBreakdown: breakdown, LatencyBound: latency, MemoryOperandBytes: operandBytes, DisallowedCPUFeatures: features, CPUIDProfile: cpuid}, nil
}

func CollectDirectory(directory string, rootPID int) Report {
	streams, err := readStreams(directory)
	if err != nil {
		return incomplete(err.Error())
	}
	report, err := aggregate(streams, rootPID)
	if err != nil {
		result := incomplete(err.Error())
		result.nativeCPUFeatures = report.nativeCPUFeatures
		return result
	}
	return report
}
