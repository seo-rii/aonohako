"""Check raw CPU accounting and link each latency diagnostic to the registered host and trace."""
from protocol import require


def check_measured_cpu(receipt):
    ns, ms = receipt["cpu_time_ns"], receipt["cpu_time_ms"]
    require(type(ns) is int and 0 < ns < 2**64 and type(ms) is int and ms == (ns + 999_999) // 1_000_000
            and not receipt.get("cpu_time_normalization") and not receipt.get("cpu_time_sampling"),
            "invalid raw measured CPU time")
    accounting = receipt["cpu_accounting"]
    peak, user, system, baseline, waited = (accounting[k] for k in (
        "process_clock_peak_ns", "wait_user_ns", "wait_system_ns", "rusage_baseline_ns", "wait_target_ns"))
    require(all(type(n) is int and 0 <= n < 2**64 for n in (peak, user, system, baseline, waited)),
            "invalid raw CPU accounting counter")
    source = accounting["selected_source"]
    # finalizeWaitCPUTime sets wait_target_ns only in the no-cgroup path.
    # The raw user/system observations may still be present on a cgroup run.
    if waited or source == "wait_rusage":
        require(waited == user + system - baseline, "inconsistent measured wait accounting")
    if source == "cgroup":
        # No separate cgroup-reading field is exported in this receipt.
        require(ns >= max(peak, waited), "cgroup CPU below measured target CPU")
    else:
        require(source in ("wait_rusage", "process_clock") and
                ns == (waited if source == "wait_rusage" else peak) == max(waited, peak),
                "invalid measured CPU source or total")


def check_latency_evidence(document, phase, arm, boot_sha256, plan, checks):
    require((document["phase"], document["arm"], document["boot_sha256"], document["image"],
             document["engine"], document["upstream"], document["files"]) ==
            (phase, arm, boot_sha256, plan["image"], plan["engine"], plan["upstream"], plan["files"]),
            "latency diagnostic differs from the registered guest or installation")
    for seq, row in enumerate(document["rows"]):
        check_measured_cpu(row["receipt"])
        # The collector stores one window per row with its original sequence
        # number. The pilot parser expects windows numbered from zero.
        header, sep, events = row["trace"].partition("\n")
        require(sep and header == f"# trial {seq}", "out-of-order latency kernel trace window")
        windows = checks.read_windows(("# trial 0\n" + events).encode())
        require(len(windows) == 1, "multiple latency kernel trace windows")
        checks.check_placement({"kind": "raw", "execs": row["execs"]}, windows[0])
