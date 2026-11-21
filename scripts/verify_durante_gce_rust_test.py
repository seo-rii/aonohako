import copy
import json
import unittest

import verify_durante_gce_rust as replay


class ClockComparisonTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        protocol = replay.read_pinned(replay.ROOT / "docs/durante/gce-pinned-rust-v10/protocol.json", replay.PROTOCOL_SHA256)
        cls.inputs = replay.reference_inputs(protocol)
        _, cls.baseline = replay.measurement(replay.CLOUD_RUN, *cls.inputs, None)

    def test_known_baseline_round_trip(self):
        models = self.baseline["clocks"]["models"]
        self.assertEqual({name: m["heldout_within_error_limit"] for name, m in models.items()},
                         {"common_v10": 3, "short_rust": 0, "cloud_run_long_rust": 4, "gce_long_rust": 4})
        self.assertEqual(self.baseline["integer4_fill1_bound"]["minimum_possible_worst_error_ppm"], 525964)
        comparison = self.baseline["native_comparison"]
        self.assertEqual(comparison["platforms"]["gce"], comparison["platforms"]["cloud_run"])
        self.assertTrue(all(c["median_change_ns"] == c["spread_change_ppm"] == 0 for c in comparison["cases"].values()))

    def test_nonpositive_fit_never_becomes_zero_error_model(self):
        protocol, history, data, _, frozen = self.inputs
        cases, groups = replay.collect_cases(data, replay.LANGUAGE, protocol, history)
        clocks = replay.clock_comparison(cases, groups, frozen, {"passed": False}, protocol["thresholds"])
        self.assertEqual(clocks["models"]["gce_long_rust"], {"fit_produced_coefficients": False})
        self.assertNotIn("gce_long_rust", clocks["predictions"])


class PlacementTests(unittest.TestCase):
    def setUp(self):
        self.sample = {"workload_id": "copy", "scale": 2, "repetition": 1}
        snap = {"cpuset_requested": "1", "cpuset_effective": "1", "allowed_cpus": [1],
                "start_or_end_cpu": 1, "observer_allowed_cpus": [0], "init_pid": 9, "cgroup_path_sha256": "e" * 64}
        stats = dict(entries=20, overrun=0, commit_overrun=0, dropped_events=0, consumed_events=0)
        self.affinity = {
            "schema": 1, "selected_cpu": 1, "housekeeping_cpus": [0], "before": snap, "after": copy.deepcopy(snap),
            "trace": {"cpus": [0, 1], "lineage_cpu_set": [1], "raw_sha256": "a" * 64, "execs": 6,
                      "trace_clock": "mono", "root_pid": 7, "births": 10, "exits": 10, "cgroup_moves": 0,
                      "per_cpu": {"cpu0": stats, "cpu1": copy.deepcopy(stats)}},
            "sampler": {"polls": 11, "unique_tids": 6, "root_observed": True,
                        "allowed_cpu_sets": [[1]], "observed_cpu_set": [1]},
            "launches": [dict(self.sample, phase=phase, program=program, pid=11+i, timestamp_ns=50+i,
                              triplet_index=0, cpu=1, exec_path_sha256=replay.digest(
                                  b"/opt/durante/dynamorio/bin64/drrun" if phase == "metered" else b"Main"))
                         for i, (phase, program) in enumerate((("native_before", "Main"), ("metered", "drrun"),
                                                              ("native_after", "Main")))],
        }

    def verify(self):
        replay.check_affinity(self.affinity, 1, [0], [self.sample])

    def test_launches_must_match_actual_phase_order_and_cpu(self):
        self.verify()
        self.affinity["launches"][1]["cpu"] = 0
        with self.assertRaisesRegex(RuntimeError, "CPU launch"):
            self.verify()
        self.affinity["launches"][1]["cpu"] = 1
        self.affinity["launches"][1]["program"] = "Main"
        with self.assertRaisesRegex(RuntimeError, "reordered workload"):
            self.verify()

    def test_rebuild_launches_even_when_metered_child_execs_main(self):
        trace = self.affinity["trace"]
        src = "d" * 64
        native = replay.digest(b"Main")
        run = replay.digest(b"/opt/durante/dynamorio/bin64/drrun")
        loader = replay.digest(b"/opt/durante/dynamorio/bin64/../lib64/release/libdynamorio.so")
        events = [
            ["exec", 40, 1, 7, 7, "durante-reference", src],
            ["fork", 49, 1, 7, 11], ["exec", 50, 1, 11, 11, "Main", native], ["exit", 50, 1, 11],
            ["fork", 50, 1, 7, 12], ["exec", 51, 1, 12, 12, "drrun", run],
            ["exec", 51, 1, 12, 12, "other", loader],
            ["fork", 51, 1, 12, 20], ["exec", 51, 1, 20, 20, "Main", native],
            ["exit", 51, 1, 20], ["exit", 51, 1, 12],
            ["fork", 51, 1, 7, 13], ["exec", 52, 1, 13, 13, "Main", native], ["exit", 53, 1, 13],
            ["exit", 54, 1, 7],
        ]
        tree = {"schema": 1, "events": events, "raw_trace_sha256": trace["raw_sha256"],
                "root_attach": [38, 0, 7, self.affinity["before"]["cgroup_path_sha256"]]}
        trace.update(lineage_rows=len(events), births=4, execs=6, exits=5, metered_image_execs={"libdynamorio.so": 1})
        def verify_tree():
            raw = json.dumps(tree).encode()
            trace["lineage_sha256"] = replay.digest(raw)
            replay.check_lineage(raw, self.affinity)

        verify_tree()
        events[6][-1] = "f" * 64
        with self.assertRaisesRegex(RuntimeError, "DynamoRIO loader"):
            verify_tree()
        events[6][-1] = loader
        events[7][2] = 0
        with self.assertRaisesRegex(RuntimeError, "outside the benchmark CPU"):
            verify_tree()

    def test_lost_events_or_widened_cpuset_break_attestation(self):
        self.affinity["trace"]["per_cpu"]["cpu0"]["overrun"] = 1
        with self.assertRaisesRegex(RuntimeError, "lost CPU"):
            self.verify()
        self.affinity["trace"]["per_cpu"]["cpu0"]["overrun"] = 0
        self.affinity["after"]["allowed_cpus"] = [0, 1]
        with self.assertRaisesRegex(RuntimeError, "single-CPU"):
            self.verify()


if __name__ == "__main__":
    unittest.main()
