"""Diagnostic PyPy JIT IR mapping, never a full-program or retired-instruction meter."""
import re

TOKEN = re.compile(r"TargetToken\((\d+)\)")
GUARD = re.compile(r"<Guard0x([0-9a-fA-F]+)>")
META = frozenset(("debug_merge_point", "label", "increment_debug_counter", "enter_portal_frame", "leave_portal_frame", "jit_debug"))


def token_number(text):
    matches = TOKEN.findall(text)
    if len(matches) != 1:
        raise ValueError("ambiguous or missing TargetToken")
    return int(matches[0])


def encode_counts(counts):
    return [{"kind": kind, "number": number, "count": count}
            for (kind, number), count in sorted(counts.items())]


def delta_counts(before, after):
    result = {}
    for key in set(before) | set(after):
        value = after.get(key, 0) - before.get(key, 0)
        if value < 0:
            raise ValueError("counter reset or disappearance")
        if key[0] not in ("e", "l", "b") or not isinstance(key[1], int):
            raise ValueError("unknown counter identity")
        result[key] = value
    return result


def build_segments(trace, harness_files):
    """Split at actual LABELs: entry + each label + bridge have disjoint IR lists.

    Raw entry counts do not reveal uncompiled guard exits. We expose a prefix
    floor and whole-segment ceiling under an entry/guard model, NOT exact bounds
    on CPU work. Native residual calls, warmup and backend optimizations remain
    outside this IR metric.
    """
    kind = "b" if trace["type"] == "bridge" else "e"
    number = trace["bridge_no"] if kind == "b" else trace["loop_no"]
    source = trace.get("source")
    segments = []
    segment = {"kind": kind, "number": number, "ops": [], "trace_index": trace["index"]}
    for op in trace["ops"]:
        name = op["name"]
        if name == "label":
            segments.append(segment)
            segment = {"kind": "l", "number": op["token"], "ops": [], "trace_index": trace["index"]}
            continue
        if name == "debug_merge_point":
            source = op.get("source")
            continue
        if name in META:
            continue
        filename = source[0] if source else None
        owner = "harness" if filename in harness_files else "unknown" if filename is None else "application_or_stdlib"
        segment["ops"].append({"name": name, "owner": owner, "source": filename,
                                "guard_id": op.get("guard_id")})
    segments.append(segment)
    return segments


def summarize(traces, counts, harness_files):
    mapped = {}
    errors = []
    for trace in traces:
        try:
            for segment in build_segments(trace, harness_files):
                key = (segment["kind"], segment["number"])
                if key in mapped:
                    raise ValueError("duplicate segment counter identity")
                mapped[key] = segment
        except (KeyError, TypeError, ValueError) as exc:
            errors.append({"trace_index": trace.get("index"), "type": type(exc).__name__, "message": str(exc)})
    rows = []
    upper = lower = 0
    mapped_hits = unmapped_hits = harness_hits = 0
    calls = guards = unknown_source_ops = 0
    unmapped = []
    for key, count in sorted(counts.items()):
        if count == 0:
            continue
        if key not in mapped:
            unmapped.append({"kind": key[0], "number": key[1], "count": count})
            unmapped_hits += count
            continue
        segment = mapped[key]
        n = prefix = 0
        first_guard_seen = False
        segment_guards = segment_calls = segment_unknown = 0
        for op in segment["ops"]:
            included = op["owner"] != "harness"
            if included:
                n += 1
                if not first_guard_seen:
                    prefix += 1
                segment_unknown += op["owner"] == "unknown"
                segment_calls += "call" in op["name"]
            if op["name"].startswith("guard_"):
                first_guard_seen = True
                segment_guards += 1
        # Count after splitting labels; do not multiply an entire trace by both
        # its entry count and loop-label count, or count unrolling a second time.
        upper += n * count
        lower += prefix * count
        mapped_hits += count
        harness_hits += count if n == 0 else 0
        guards += segment_guards * count
        calls += segment_calls * count
        unknown_source_ops += segment_unknown * count
        rows.append({"kind": key[0], "number": key[1], "trace_index": segment["trace_index"],
                     "entries": count, "non_harness_ir_ops": n,
                     "prefix_through_first_guard_ir_ops": prefix,
                     "guards_per_segment": segment_guards,
                     "residual_or_assembler_calls_per_segment": segment_calls,
                     "unknown_source_ir_ops": segment_unknown})
    return {
        "name": "jit_segment_ir_work_estimate",
        "value": upper,
        "guard_prefix_floor_estimate": lower,
        "segment_entry_ceiling_estimate": upper,
        "full_program_coverage": False,
        "available": bool(upper) and not errors,
        "interpretation": "Disjoint trace/label/bridge segment entry counts times optimized IR operations. Whole segment charged even on early guard exits; floor/ceiling are model estimates, not proven CPU-work bounds.",
        "mapped_counter_hits": mapped_hits,
        "unmapped_counter_hits": unmapped_hits,
        "harness_only_counter_hits": harness_hits,
        "guard_entry_weighted_count": guards,
        "residual_call_entry_weighted_count": calls,
        "unknown_source_entry_weighted_ir_ops": unknown_source_ops,
        "unmapped_counters": unmapped,
        "segments": rows,
        "mapping_errors": errors,
        "coverage_gaps": ["interpreted execution and JIT warmup", "uncompiled guard exits and partial segments",
                          "work inside residual/native calls", "GC and JIT compiler work",
                          "traces compiled before hook or recursively inside hook", "backend optimizations after IR mapping"],
    }
