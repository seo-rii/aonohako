package workmeter

const Metric = "bb_entry_app_instructions"
const Authority = "diagnostic_only"
const Scope = "process_tree_user_code"

// Report deliberately keeps work units separate from CPU time and RSS. A
// complete report verifies lifecycle consistency, not adversarial integrity.
type Report struct {
	Schema                int     `json:"schema"`
	ClientVersion         int     `json:"client_version"`
	Scope                 string  `json:"scope"`
	REPEnabled            bool    `json:"rep_bytes_enabled"`
	EngineProfile         string  `json:"engine_profile"`
	Metric                string  `json:"metric"`
	Authority             string  `json:"authority"`
	Complete              bool    `json:"complete"`
	Quality               string  `json:"quality"`
	Reason                string  `json:"reason,omitempty"`
	Instructions          *uint64 `json:"instructions,omitempty"`
	REPBytes              *uint64 `json:"rep_bytes,omitempty"`
	REPEntries            *uint64 `json:"rep_entries,omitempty"`
	UnsupportedREPEntries *uint64 `json:"unsupported_rep_entries,omitempty"`
	Processes             int     `json:"processes"`
	Images                int     `json:"images"`
	SignalsObserved       uint64  `json:"signals_observed"`
	Family                string  `json:"family"`
	DispatchProfile       string  `json:"dispatch_profile"`
	HostLibc              string  `json:"host_glibc_version,omitempty"`
	TargetLibcAttested    bool    `json:"target_libc_attested"`
}

func incomplete(reason string) Report {
	return Report{Schema: 1, ClientVersion: 1, Metric: Metric, Authority: Authority, Scope: Scope, Quality: "incomplete", Reason: reason}
}
