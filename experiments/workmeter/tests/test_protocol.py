"""Independent diagnostic-report and execution-boundary contracts."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import protocol
from validate import check_batch, check_row


class NativeReportTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name); self.meta = self.root/'metadata'; self.meta.mkdir()
        self.identifier = 'a'*32
        raw = json.dumps({'blocks': [[0, 3, 'work', 0], [1, 7, 'work', 1]]}).encode()
        (self.meta/(self.identifier+'.json')).write_bytes(raw)
        (self.root/'metadata-manifest.json').write_text(json.dumps({'schema': 1,
            'files': {self.identifier+'.json': hashlib.sha256(raw).hexdigest()}}))
        self.old = protocol.META; protocol.META = self.meta
        protocol.metadata_manifest.cache_clear(); protocol.block_weights.cache_clear()

    def tearDown(self):
        protocol.META = self.old
        protocol.metadata_manifest.cache_clear(); protocol.block_weights.cache_clear()
        self.tmp.cleanup()

    def report(self):
        return {'schema': 1, 'pid': 123, 'valid': True, 'metric': 'llvm_ir_block_work',
            'work_units': 20, 'flags': 0, 'modules_seen': 1, 'nonzero_blocks': 2,
            'authority': 'diagnostic_only', 'validity_scope': 'counter_integrity_only',
            'model_version': 'llvm-ir-v1', 'modules': [{'metadata_id': self.identifier,
                'blocks': 2, 'counts': [[0, 2], [1, 2]]}]}

    def test_recompute_and_preserve_scope(self):
        result = protocol.summarize_metric(self.report(), 'ir_count', 123)
        self.assertTrue(result['vector_verified']); self.assertEqual(result['work_units'], 20)
        self.assertEqual(result['authority'], 'diagnostic_only')
        self.assertEqual(result['validity_scope'], 'counter_integrity_only')

    def test_distinct_arrays_can_share_metadata(self):
        r = self.report(); r['modules'].append(copy.deepcopy(r['modules'][0]))
        r.update(modules_seen=2, nonzero_blocks=4, work_units=40)
        self.assertEqual(protocol.summarize_metric(r, 'ir_count', 123)['work_units'], 40)

    def test_invalid_counts_and_identity_rejected(self):
        mutations = [lambda r: r['modules'][0]['counts'].append([0, 1]),
            lambda r: r.update(work_units=21), lambda r: r['modules'][0]['counts'][0].__setitem__(1, -1),
            lambda r: r['modules'][0]['counts'][0].__setitem__(1, 2**64),
            lambda r: r.update(pid=999), lambda r: r.update(authority='authoritative'),
            lambda r: r.update(model_version='unknown'), lambda r: r.update(flags=8)]
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index):
                r = self.report(); mutate(r)
                with self.assertRaises(ValueError): protocol.summarize_metric(r, 'ir_count', 123)

    def test_metadata_corruption_rejected(self):
        (self.meta/(self.identifier+'.json')).write_text('{}')
        with self.assertRaises(ValueError): protocol.summarize_metric(self.report(), 'ir_count', 123)

    def test_only_plain_control_can_report_valid_zero(self):
        r = self.report(); r.update(work_units=0, nonzero_blocks=0); r['modules'][0]['counts'] = []
        with self.assertRaises(ValueError): protocol.summarize_metric(r, 'ir_count', 123)
        self.assertEqual(protocol.summarize_metric(r, 'source_plain', 123)['work_units'], 0)


class ExecutionTests(unittest.TestCase):
    def workload(self, policy='valid', kind='exit', code=0):
        w = {'id': 'boundary', 'language': 'python', 'family': 'boundary',
             'expected_execution': {'kind': kind}, 'expected_metric': {'ir_count': policy}}
        if kind == 'exit': w['expected_execution']['code'] = code
        return w

    def row(self):
        return {'workload_id': 'boundary', 'arm': 'ir_count', 'copy_index': 0,
            'pid': 123, 'termination': 'exit', 'exit_code': 0, 'timed_out': False,
            'stdout_matches_tokens': True, 'output_truncated': False, 'descendants_reaped': [],
            'metric_error': None, 'metric': {'pid': 123, 'valid': True, 'work_units': 10},
            'raw_report': {'present': True}, 'authoritative_time': False}

    def test_expected_error_is_boundary_success_not_program_success(self):
        r = self.row(); r['exit_code'] = 7
        self.assertEqual(check_row(r, self.workload(code=7)), [])
        self.assertFalse(protocol.valid_execution(r))
        self.assertIn('wrong_exit_code', check_row(r, self.workload()))

    def test_wall_kill_has_unavailable_work(self):
        r = self.row(); r.update(termination='wall_limit', exit_code=-signal.SIGKILL,
                                timed_out=True, metric=None, raw_report=None)
        w = self.workload(policy='missing', kind='wall_limit')
        self.assertEqual(check_row(r, w), [])
        r['metric'] = {'pid': 123, 'valid': True, 'work_units': 0}
        self.assertIn('unexpected_report', check_row(r, w))

    def test_unsupported_thread_is_invalid_not_zero(self):
        r = self.row(); r['exit_code'] = 1
        r['metric'] = {'pid': 123, 'valid': False, 'flags': 8, 'work_units': None}
        w = self.workload(policy='unsupported_threads', code=1)
        self.assertEqual(check_row(r, w), [])
        r['metric']['work_units'] = 0
        self.assertIn('unsupported_threads_not_marked_invalid', check_row(r, w))

    def test_duplicate_copy_and_changed_instance_rejected(self):
        w = self.workload(); req = {'workload_id': 'boundary', 'iteration': 0,
                                    'parallelism': 2, 'arms': ['ir_count']}
        r = self.row(); result = {**req, 'environment': {'instance_id': 'i'},
                                  'rows': [r, copy.deepcopy(r)]}
        self.assertIn('missing_or_duplicate_rows', check_batch(result, w, req))
        result['rows'][1]['copy_index'] = 1
        self.assertEqual(check_batch(result, w, req), [])
        self.assertIn('instance_changed', check_batch(result, w, req, 'another'))

    def test_jit_missing_is_allowed_but_complete_coverage_claim_rejected(self):
        r = self.row(); r.update(arm='jit_mapped', metric=None, raw_report=None)
        w = self.workload(); w['language'] = 'pypy'
        self.assertEqual(check_row(r, w), [])
        r['metric'] = {'pid': 123, 'valid': True, 'work_units': None, 'full_program_coverage': True}
        self.assertIn('jit_coverage_claim', check_row(r, w))


class RunnerRegressionTests(unittest.TestCase):
    def test_attestation_preserves_stdout_expectation(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); fixtures = root/'fixtures'; fixtures.mkdir()
            (fixtures/'manifest.json').write_text('{"workloads": []}')
            (fixtures/'input').write_bytes(b''); (fixtures/'expected').write_bytes(b'answer\n')
            (fixtures/'Main.py').write_text('print("answer")\n')
            provenance = root/'cpython/provenance'; provenance.mkdir(parents=True)
            hashes = list(range(8))
            (provenance/'hashseed-smoke.json').write_text(json.dumps({'rows': [
                {'fixed': {'1': {'hashes': hashes}}}]}))
            with patch.dict(os.environ, {'WORKMETER_ROOT': str(root)}), \
                    patch('shutil.which', return_value=None), \
                    patch('ctypes.CDLL', return_value=SimpleNamespace(prctl=lambda *a: 0)):
                spec = importlib.util.spec_from_file_location('_review_runner', ROOT/'runner.py')
                runner = importlib.util.module_from_spec(spec); spec.loader.exec_module(runner)
            child = SimpleNamespace(pid=123, returncode=None)
            def popen(command, **kw):
                kw['stdout'].write(b'answer\n')
                Path(kw['env']['WM_ATTEST_PATH']).write_text(json.dumps({'pid': 123,
                    'hashes': hashes, 'isolated': 1, 'ignore_environment': 1,
                    'no_site': 1, 'safe_path': True}))
                return child
            usage = SimpleNamespace(ru_utime=.1, ru_stime=.01, ru_maxrss=1024)
            w = {'id': 'example', 'language': 'python', 'family': 'original', 'submission_id': None,
                'argv': ['python3', '-I', '-S', '-c', 'pass', 'Main.py'], 'primary_argv_index': 5,
                'primary': 'Main.py', 'cwd': '.', 'input': 'input', 'expected': 'expected'}
            with patch.object(runner.shutil, 'which', return_value=sys.executable), \
                    patch.object(runner.subprocess, 'Popen', side_effect=popen), \
                    patch.object(runner.os, 'chown'), patch.object(runner.os, 'killpg'), \
                    patch.object(runner.os, 'wait4', return_value=(123, 0, usage)), \
                    patch.object(runner.os, 'waitpid', side_effect=ChildProcessError), \
                    patch.object(runner.signal, 'signal'), patch.object(runner.signal, 'setitimer'):
                result = runner.run(w, 'source_plain')
            self.assertTrue(result['stdout_matches_tokens']); self.assertIsNone(result['attestation_error'])
            self.assertEqual(result['cpu_ms'], 110)


if __name__ == '__main__': unittest.main()
