import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT))
import client
import prepare

class ToolTests(unittest.TestCase):
    def test_context_contains_only_public_generated_fixtures_and_exact_hashes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);runtime=root/'runtime.json'
            runtime.write_text(json.dumps({'image':'test-image','metadata_manifest_sha256':'0'*64}))
            output=root/'context'
            hashes=prepare.prepare(output,runtime)
            manifest=json.loads((output/'fixtures/manifest.json').read_bytes())
            self.assertEqual(len(manifest['workloads']),58)
            self.assertTrue(all(w.get('kind')!='submission' for w in manifest['workloads']))
            for name,digest in hashes.items():
                self.assertEqual(hashlib.sha256((output/name).read_bytes()).hexdigest(),digest)
            self.assertTrue((output/'launch').stat().st_mode & 0o111)
            with self.assertRaises(ValueError):prepare.prepare(output,runtime)

    def test_transport_failure_is_persisted_without_retry(self):
        workloads=[{'id':'test','language':'python','family':'boundary','expected_execution':{'kind':'exit','code':0}}]
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            with patch('urllib.request.urlopen',side_effect=OSError('not available')) as call:
                result=client.collect(root,'workmeter-01','http://localhost',workloads,1,None)
            self.assertEqual(call.call_count,1)
            self.assertEqual(len(result['failures']),1)
            saved=json.loads((root/'workmeter-01-responses.ndjson').read_text())
            self.assertEqual(saved['request_error'],'OSError')
            self.assertEqual(saved['request']['workload_id'],'test')

    def test_stress_is_not_added_to_every_case(self):
        workloads=[{'id':'short','language':'python','scale':1},
                   {'id':'large','language':'python','scale':4}]
        schedule=client.requests(workloads,'workmeter-01',3)
        self.assertEqual(len(schedule),9)
        self.assertTrue(all(r['parallelism']==1 for r in schedule if r['workload_id']=='short'))

if __name__=='__main__':unittest.main()
