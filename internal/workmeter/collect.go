package workmeter

import (
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
	Schema                int    `json:"schema"`
	ClientVersion         int    `json:"client_version"`
	Authority             string `json:"authority"`
	Metric                string `json:"metric"`
	PID                   int    `json:"pid"`
	Segment               string `json:"segment"`
	Seq                   uint64 `json:"seq"`
	Event                 string `json:"event"`
	SDKVersion            string `json:"sdk_version"`
	Origin                string `json:"origin"`
	ParentPID             int    `json:"parent_pid"`
	ParentSegment         string `json:"parent_segment"`
	REPEnabled            bool   `json:"rep_bytes_enabled"`
	ChildPID              int    `json:"child_pid"`
	Syscall               int    `json:"syscall"`
	Supported             bool   `json:"supported"`
	Result                int64  `json:"result"`
	Instructions          uint64 `json:"instructions"`
	REPBytes              uint64 `json:"rep_bytes"`
	REPEntries            uint64 `json:"rep_entries"`
	UnsupportedREPEntries uint64 `json:"unsupported_rep_entries"`
	Signals               uint64 `json:"signals_observed"`
	Valid                 bool   `json:"valid"`
	AccountingError       bool   `json:"accounting_error"`
	ThreadsStarted        uint64 `json:"threads_started"`
	ThreadsFinished       uint64 `json:"threads_finished"`
	ThreadsComplete       bool   `json:"threads_complete"`
	Quality               string `json:"quality"`
	ExitSeen              bool   `json:"app_exit_seen"`
	ExitCode              uint64 `json:"app_exit_code"`
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
	if err := json.Unmarshal(data, &row); err != nil {
		return row, errors.New("invalid report field type")
	}
	required := []string{"schema", "client_version", "authority", "metric", "pid", "segment", "seq", "event"}
	switch row.Event {
	case "start":
		required = append(required, "sdk_version", "origin", "parent_pid", "parent_segment", "rep_bytes_enabled")
	case "spawn":
		required = append(required, "child_pid", "syscall", "supported")
	case "exec_failed":
		required = append(required, "syscall", "result")
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
	if row.ExitCode > 255 || (!isExec && !row.ExitSeen) {
		return counts, errors.New("application exit unavailable")
	}
	return counts, nil
}

func aggregate(streams map[int][]event, rootPID int) (Report, error) {
	if !validPID(rootPID) || len(streams) == 0 || len(streams) > maxProcesses {
		return Report{}, errors.New("root PID or process count invalid")
	}
	if _, ok := streams[rootPID]; !ok {
		return Report{}, errors.New("root process report missing")
	}
	segments := map[string]bool{}
	processes := map[int]lineage{}
	edges := map[int]lineage{}
	var totals [5]uint64
	images := 0
	repKnown, repEnabled := false, false
	sum := func(row event, isExec bool) error {
		counts, err := terminalCounters(row, isExec, repEnabled)
		if err != nil {
			return err
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
				repKnown = true
				repEnabled = row.REPEnabled
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
	return Report{Schema: 1, ClientVersion: 1, Metric: Metric, Authority: Authority, Scope: Scope, REPEnabled: repEnabled, Complete: true, Quality: quality, Instructions: &totals[0], REPBytes: &totals[1], REPEntries: &totals[2], UnsupportedREPEntries: &totals[3], SignalsObserved: totals[4], Processes: len(processes), Images: images}, nil
}

func CollectDirectory(directory string, rootPID int) Report {
	streams, err := readStreams(directory)
	if err != nil {
		return incomplete(err.Error())
	}
	report, err := aggregate(streams, rootPID)
	if err != nil {
		return incomplete(err.Error())
	}
	return report
}
