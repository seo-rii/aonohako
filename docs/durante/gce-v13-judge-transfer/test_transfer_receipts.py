from copy import deepcopy
import unittest

from audit_transfer_receipts import check_latency_evidence, check_measured_cpu
from verify import V12


def cpu_receipt():
    return {"cpu_time_ns": 2_000_001, "cpu_time_ms": 3,
            "cpu_accounting": {"selected_source": "wait_rusage", "wait_user_ns": 2_500_001,
                               "wait_system_ns": 0, "rusage_baseline_ns": 500_000,
                               "wait_target_ns": 2_000_001, "process_clock_peak_ns": 2_000_000}}


class TransferReceiptTest(unittest.TestCase):
    def test_measured_cpu_selection(self):
        for source, ns, waited in (("wait_rusage", 2_000_001, 2_000_001),
                                   ("process_clock", 2_000_000, 0),
                                   ("cgroup", 2_750_000, 0)):
            with self.subTest(source=source):
                r = cpu_receipt()
                r.update(cpu_time_ns=ns, cpu_time_ms=(ns + 999_999) // 1_000_000)
                r["cpu_accounting"].update(selected_source=source, wait_target_ns=waited)
                check_measured_cpu(r)
        for changes in ({"wait_system_ns": 1}, {"selected_source": "cgroup", "process_clock_peak_ns": 5_000_000},
                        {"selected_source": "unknown"}, {"process_clock_peak_ns": -1}):
            with self.subTest(changes=changes):
                r = cpu_receipt()
                r["cpu_accounting"].update(changes)
                with self.assertRaises(ValueError):
                    check_measured_cpu(r)
        r = cpu_receipt()
        r["cpu_time_ms"] = 2
        with self.assertRaises(ValueError):
            check_measured_cpu(r)

    def test_latency_identity_and_kernel_windows(self):
        plan = {"engine": "engine", "upstream": "commit", "image": "image", "files": {"server": "server"}}
        path = "/opt/durante/dynamorio/bin64/drrun"
        doc = dict(plan, phase="repeat", arm="v13", boot_sha256="boot", rows=[
            {"receipt": cpu_receipt(), "execs": [{"cpu": 1, "path": path, "pid": 12 + i}],
             "trace": f"# trial {i}\nshell [001] sched_process_exec: filename={path} pid={12+i} old_pid=12\n"}
            for i in (0, 1)])

        def verify(doc):
            check_latency_evidence(doc, "repeat", "v13", "boot", plan, V12)

        verify(doc)
        for key in ("phase", "boot_sha256", "image", "files"):
            with self.subTest(key=key), self.assertRaises(ValueError):
                verify(dict(doc, **{key: "other"}))
        for old, new in (("# trial 1", "# trial 0"), ("pid=13", "pid=42"), ("[001]", "[002]")):
            changed = deepcopy(doc)
            changed["rows"][1]["trace"] = changed["rows"][1]["trace"].replace(old, new)
            with self.subTest(replacement=new), self.assertRaises(ValueError):
                verify(changed)


if __name__ == "__main__":
    unittest.main()
