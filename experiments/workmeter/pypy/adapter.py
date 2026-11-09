"""Compact parent-protocol projection; raw reports remain the source of evidence."""


def summarize(report, arm):
    if not isinstance(report, dict):
        return {"schema": 1, "pid": None, "valid": False, "metric": None,
                "work_units": None, "reason": "missing_report"}
    mode = {"jit_count": "counts", "jit_mapped": "mapped"}.get(arm)
    valid = (report.get("schema") == 1 and report.get("mode") == mode
             and report.get("valid") is True and isinstance(report.get("pid"), int))
    result = {"schema": 1, "pid": report.get("pid"), "valid": valid,
              "metric": "jit_segment_ir_work_estimate" if mode == "mapped" else "jit_counter_diagnostic",
              "work_units": None, "reason": None if valid else "invalid_report",
              "coverage": "partial JIT work only; not full-program work or CPU seconds",
              "full_program_coverage": False,
              "snapshot_nonempty": report.get("counter_snapshot_nonempty"),
              "raw_delta_counter_count": len(report.get("delta_counts", [])),
              "jit_counters_delta": report.get("jit_counters_delta", {}),
              "trace_capture_capped": report.get("trace_capture_capped", False)}
    if mode == "counts":
        result["reason"] = "cost_control_no_ir_mapping" if valid else result["reason"]
        result["coverage_gaps"] = ["no mapping from overlapping entry/label/bridge counters to work",
                                   "interpreted execution and native/GC/compiler work"]
        return result
    metric = report.get("metric") or {}
    errors = metric.get("mapping_errors", [])
    if errors:
        result.update(valid=False, reason="mapping_error")
    value = metric.get("value")
    if result["valid"] and metric.get("available") is True and isinstance(value, int) and not isinstance(value, bool) and value > 0:
        result["work_units"] = value
    elif result["valid"]:
        result["reason"] = "coverage_gap_no_mapped_work"
    for key in ("guard_prefix_floor_estimate", "segment_entry_ceiling_estimate",
                "mapped_counter_hits", "unmapped_counter_hits", "harness_only_counter_hits",
                "guard_entry_weighted_count", "residual_call_entry_weighted_count",
                "unknown_source_entry_weighted_ir_ops", "coverage_gaps"):
        result[key] = metric.get(key)
    mapped, unmapped = metric.get("mapped_counter_hits", 0), metric.get("unmapped_counter_hits", 0)
    result["mapped_counter_hit_fraction"] = mapped/(mapped+unmapped) if mapped+unmapped else None
    result["mapping_fraction_interpretation"] = "Fraction of counter hits mapped, not fraction of program CPU/work covered"
    result["mapped_segments"] = len(metric.get("segments", []))
    result["mapped_traces"] = len(report.get("traces", []))
    return result
