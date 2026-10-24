"""Regression tests for verification performed only in Go runtime builds."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from runtime_incremental_test import impact, fp, m


class NestedGoVerificationTests(unittest.TestCase):
    def setUp(self):
        self.ctx = {'repository': 'owner/repo', 'repository_id': 1, 'event': 'pull_request',
                    'pr_number': 44, 'head_repository_id': 2, 'head_branch': 'feature', 'base_branch': 'main',
                    'run_id': '100', 'run_attempt': '1', 'cache_write': False}
        self.inputs = {'workflow': fp('a'), 'sandbox': fp('b'), 'go_module': fp('c')}

    def go_impact(self):
        data = impact()
        data['production'].append({'name': 'type-g', 'languages': 'python,go', 'fingerprint': fp('e')})
        data['ci'].append({'name': 'ci-go', 'languages': 'go', 'fingerprint': fp('f')})
        return data

    def test_nested_go_inputs_select_only_go_verifications(self):
        data = self.go_impact()
        base = m.plan(data, self.inputs, '1' * 40, {**self.ctx, 'run_id': '90'})
        result = m.plan(data, {**self.inputs, 'go_module': fp('d')}, '2' * 40, self.ctx, base)
        self.assertEqual([x['name'] for x in result['outputs']['production_matrix']], ['type-g'])
        self.assertEqual([x['name'] for x in result['outputs']['ci_matrix']], ['ci-go'])
        self.assertFalse(result['outputs']['sandbox_required'])
        self.assertTrue(all(not x['changed'] for f in ('ci', 'production') for x in result[f]))
        unchanged = m.plan(data, {**self.inputs, 'go_module': fp('d')}, '3' * 40, self.ctx, result)
        self.assertFalse(unchanged['outputs']['has_any_runtime'])

    def test_go_detection_is_token_based(self):
        data = impact()
        data['ci'].append({'name': 'ci-golfscript', 'languages': 'golfscript', 'fingerprint': fp('e')})
        base = m.plan(data, self.inputs, '1' * 40, {**self.ctx, 'run_id': '90'})
        result = m.plan(data, {**self.inputs, 'go_module': fp('d')}, '2' * 40, self.ctx, base)
        self.assertFalse(result['outputs']['has_any_runtime'])

    def test_real_nested_go_test_add_edit_delete_cannot_escape_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                return subprocess.check_output(['git', '-C', directory, *args], stderr=subprocess.DEVNULL)
            git('init', '-b', 'main')
            git('config', 'user.name', 'Regression Test')
            git('config', 'user.email', 'test@example.invalid')
            Path(directory, 'go.mod').write_text('module fixture\n\ngo 1.20\n')
            Path(directory, 'root.go').write_text('package fixture\n')
            nested = Path(directory, 'go-modules')
            nested.mkdir()
            (nested / 'go.mod').write_text('module nested\n\ngo 1.20\n')
            (nested / 'dependencies.go').write_text('package nested\n')
            git('add', '.')
            git('commit', '-m', 'baseline')
            env = {**os.environ, 'GOWORK': 'off', 'GOTOOLCHAIN': 'local', 'GOPROXY': 'off', 'GOSUMDB': 'off'}
            with patch.object(m, 'command', side_effect=lambda *args: git(*args[1:])):
                before = m.validation_inputs()
                data = self.go_impact()
                base = m.plan(data, before, '1' * 40, {**self.ctx, 'run_id': '90'})
                test = nested / 'dependencies_test.go'
                test.write_text('package nested\nimport "testing"\nfunc TestRegression(t *testing.T) { t.Fatal("nested regression") }\n')
                git('add', '.')
                git('commit', '-m', 'add failing nested test')
                added = m.validation_inputs()
                # Demonstrate why the root job cannot cover the nested module.
                root_test = subprocess.run(['go', 'test', './...'], cwd=directory, env=env, capture_output=True, timeout=60)
                nested_test = subprocess.run(['go', 'test', './...'], cwd=nested, env=env, capture_output=True, timeout=60)
                self.assertEqual(root_test.returncode, 0, root_test.stderr.decode())
                self.assertNotEqual(nested_test.returncode, 0)
                self.assertIn(b'nested regression', nested_test.stdout)
                test.write_text(test.read_text().replace('nested regression', 'edited regression'))
                git('commit', '-am', 'edit test')
                edited = m.validation_inputs()
                test.unlink()
                git('add', '-u')
                git('commit', '-m', 'remove test')
                deleted = m.validation_inputs()
            self.assertEqual(before['workflow'], added['workflow'])
            self.assertEqual(before['sandbox'], added['sandbox'])
            self.assertNotEqual(before['go_module'], added['go_module'])
            self.assertNotEqual(added['go_module'], edited['go_module'])
            self.assertEqual(before['go_module'], deleted['go_module'])
            for inputs in (added, edited, deleted):
                result = m.plan(data, inputs, '2' * 40, self.ctx, base)
                self.assertEqual([x['name'] for x in result['outputs']['production_matrix']], ['type-g'])
                self.assertEqual([x['name'] for x in result['outputs']['ci_matrix']], ['ci-go'])
                self.assertFalse(result['outputs']['sandbox_required'])
                base = result


if __name__ == '__main__':
    unittest.main()
