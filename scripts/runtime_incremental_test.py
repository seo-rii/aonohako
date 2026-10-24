import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('runtime_incremental', Path(__file__).with_name('runtime_incremental.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def fp(letter):
    return 'sha256:' + letter * 64


def impact():
    return {'production': [{'name': 'type-a', 'languages': 'rust', 'fingerprint': fp('a')},
                           {'name': 'type-i', 'languages': 'python', 'fingerprint': fp('b')}],
            'ci': [{'name': 'ci-rust', 'fingerprint': fp('c')}, {'name': 'ci-python', 'fingerprint': fp('d')}]}


class IncrementalTests(unittest.TestCase):
    def setUp(self):
        self.ctx = {'repository': 'owner/repo', 'repository_id': 1, 'event': 'pull_request',
                    'pr_number': 44, 'head_repository_id': 2, 'head_branch': 'feature', 'base_branch': 'main',
                    'run_id': '100', 'run_attempt': '1', 'cache_write': False}
        self.inputs = {'workflow': fp('a'), 'sandbox': fp('b')}
        self.base = m.plan(impact(), self.inputs, '1' * 40, {**self.ctx, 'run_id': '90'})

    def make(self, data=None, inputs=None, base=True, **kwargs):
        return m.plan(data or impact(), inputs or self.inputs, '2' * 40, self.ctx, self.base if base else None, **kwargs)

    def test_first_run_is_full(self):
        out = self.make(base=False)['outputs']
        self.assertTrue(out['has_any_runtime'])
        self.assertTrue(out['sandbox_required'])
        self.assertEqual(len(out['production_matrix']), 2)

    def test_unchanged_source_does_not_rebuild(self):
        out = self.make()['outputs']
        self.assertFalse(out['has_any_runtime'])
        self.assertFalse(out['sandbox_required'])
        self.assertEqual(out['production_matrix'], [])

    def test_one_language_change_selects_only_consumers(self):
        data = impact()
        data['production'][0]['fingerprint'] = fp('f')
        data['ci'][0]['fingerprint'] = fp('e')
        out = self.make(data)['outputs']
        self.assertEqual([x['name'] for x in out['production_matrix']], ['type-a'])
        self.assertEqual([x['name'] for x in out['ci_matrix']], ['ci-rust'])
        self.assertFalse(out['sandbox_required'])

    def test_sandbox_test_change_runs_test_without_changing_image_inventory(self):
        result = self.make(inputs={**self.inputs, 'sandbox': fp('c')})
        self.assertTrue(result['outputs']['sandbox_required'])
        self.assertFalse(result['outputs']['has_any_runtime'])
        self.assertTrue(all(not x['changed'] for f in ('ci', 'production') for x in result[f]))

    def test_workflow_change_revalidates_unchanged_images(self):
        result = self.make(inputs={**self.inputs, 'workflow': fp('c')})
        self.assertTrue(result['outputs']['has_any_runtime'])
        self.assertTrue(all(not x['changed'] and x['verify'] for f in ('ci', 'production') for x in result[f]))

    def test_algorithm_schema_change_rejects_baseline(self):
        self.base['schema_version'] = 1
        self.assertTrue(self.make()['outputs']['has_any_runtime'])

    def test_malformed_current_inventory_is_fatal(self):
        for mutate in (lambda d: d.pop('ci'), lambda d: d['production'][0].pop('fingerprint'),
                       lambda d: d['ci'].append(d['ci'][0]), lambda d: d['ci'].clear()):
            with self.subTest(mutate=mutate):
                data = impact()
                mutate(data)
                with self.assertRaises(ValueError):
                    self.make(data)

    def test_force_check_revalidates_everything(self):
        self.assertTrue(self.make(force=True)['outputs']['has_any_runtime'])

    def test_missing_profile_in_baseline_is_new_work(self):
        self.base['production'].pop(0)
        self.assertEqual([x['name'] for x in self.make()['outputs']['production_matrix']], ['type-a'])

    def test_failed_or_foreign_pr_cannot_supply_baseline(self):
        run = {'id': 90, 'event': 'pull_request', 'conclusion': 'success', 'head_branch': 'feature',
               'head_repository': {'id': 2}, 'pull_requests': [{'number': 44}]}
        self.assertTrue(m.eligible_run(run, self.ctx))
        for change in ({'conclusion': 'failure'}, {'head_repository': {'id': 3}},
                       {'pull_requests': [{'number': 45}]}, {'id': 100}, {'head_branch': 'other'}):
            self.assertFalse(m.eligible_run({**run, **change}, self.ctx))

    def test_selected_job_skip_is_not_success(self):
        snapshot = self.make(base=False)
        names = ['policy', 'unit', 'supply-chain', 'runtime-matrix', 'toolchain-summary', 'runtime-binaries',
                 'sandbox', 'image-sbom', 'mixin-smoke', 'language-smoke', 'toolchain-profile',
                 'language-smoke-cache-read', 'language-smoke-cache-write', 'toolchain-profile-cache-read', 'toolchain-profile-cache-write']
        needs = {name: {'result': 'success'} for name in names}
        m.verify_jobs(snapshot, needs)
        for name in ['runtime-binaries', 'sandbox', 'language-smoke', 'toolchain-profile-cache-read']:
            for status in ['skipped', 'failure', 'cancelled', '']:
                changed = copy.deepcopy(needs)
                changed[name]['result'] = status
                with self.subTest(name=name, status=status), self.assertRaises(ValueError):
                    m.verify_jobs(snapshot, changed)

    def test_zero_change_accepts_intentional_skips(self):
        needs = {name: {'result': 'success'} for name in ['policy', 'unit', 'supply-chain', 'runtime-matrix', 'toolchain-summary']}
        needs['runtime-binaries'] = {'result': 'skipped'}
        m.verify_jobs(self.make(), needs)

    def test_api_failure_does_not_create_empty_plan(self):
        with patch.object(m, 'api', side_effect=OSError('network unavailable')):
            self.assertIsNone(m.load_baseline(self.ctx))

    def test_real_git_merge_tree_snapshot_is_not_pr_head(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                return subprocess.check_output(['git', '-C', directory, *args], stderr=subprocess.DEVNULL).decode().strip()
            git('init', '-b', 'main')
            git('config', 'user.name', 'Regression Test')
            git('config', 'user.email', 'test@example.invalid')
            Path(directory, 'runtime').write_text('v1')
            git('add', '.')
            git('commit', '-m', 'base')
            git('branch', 'feature')
            Path(directory, 'runtime').write_text('v2')
            git('commit', '-am', 'common runtime change')
            git('checkout', 'feature')
            Path(directory, 'README').write_text('first')
            git('add', '.')
            git('commit', '-m', 'PR')
            old_head = git('rev-parse', 'HEAD')
            git('checkout', '-b', 'merge-one', 'main')
            git('merge', '--no-ff', 'feature', '-m', 'test merge one')
            old_merge = git('rev-parse', 'HEAD')
            baseline = copy.deepcopy(self.base)
            baseline['source_sha'] = old_merge
            git('checkout', 'feature')
            Path(directory, 'README').write_text('second')
            git('commit', '-am', 'docs only')
            git('checkout', '-b', 'merge-two', 'main')
            git('merge', '--no-ff', 'feature', '-m', 'test merge two')
            new_merge = git('rev-parse', 'HEAD')
            self.assertIn('runtime', git('diff', '--name-only', old_head, new_merge))
            self.assertNotIn('runtime', git('diff', '--name-only', old_merge, new_merge))
            result = m.plan(impact(), self.inputs, new_merge, self.ctx, baseline)
            self.assertFalse(result['outputs']['has_any_runtime'])
            self.assertEqual(result['outputs']['base_sha'], old_merge)

    def test_real_git_test_inputs_change_independently(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                return subprocess.check_output(['git', '-C', directory, *args], stderr=subprocess.DEVNULL)
            git('init', '-b', 'main')
            git('config', 'user.name', 'Regression Test')
            git('config', 'user.email', 'test@example.invalid')
            test = Path(directory, 'internal/execute/security_ci_test.go')
            test.parent.mkdir(parents=True)
            test.write_text('test-v1')
            git('add', '.')
            git('commit', '-m', 'test')
            with patch.object(m, 'command', side_effect=lambda *args: git(*args[1:])):
                before = m.validation_inputs()
                test.write_text('test-v2')
                git('commit', '-am', 'new security regression')
                after = m.validation_inputs()
            self.assertEqual(before['workflow'], after['workflow'])
            self.assertNotEqual(before['sandbox'], after['sandbox'])


if __name__ == '__main__':
    unittest.main()
