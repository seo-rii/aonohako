"""Shared fixed-cohort validity and metric contracts. Missing work is not zero time."""
import hashlib
import json
import re
from functools import lru_cache
from pathlib import Path

META = Path('/opt/interpreter-meter/cpython/metadata')

@lru_cache(maxsize=1)
def metadata_manifest():
    data=(META.parent/'metadata-manifest.json').read_bytes()
    return json.loads(data),hashlib.sha256(data).hexdigest()

@lru_cache(maxsize=8192)
def block_weights(identifier):
    if not isinstance(identifier,str) or not re.fullmatch('[0-9a-f]{32}',identifier):
        raise ValueError('invalid metadata id')
    manifest,_=metadata_manifest();name=identifier+'.json';data=(META/name).read_bytes()
    if hashlib.sha256(data).hexdigest()!=manifest['files'].get(name):
        raise ValueError('native metadata hash mismatch')
    return [block[1] for block in json.loads(data)['blocks']]

def verify_native_vector(report):
    modules=report.get('modules');total=0;nonzero=0
    if not isinstance(modules,list) or report.get('modules_seen')!=len(modules):
        raise ValueError('module count mismatch')
    for module in modules:
        weights=block_weights(module['metadata_id'])
        if module['blocks']!=len(weights):raise ValueError('metadata block count mismatch')
        seen=set()
        for index,count in module['counts']:
            if (type(index) is not int or index<0 or index>=len(weights) or index in seen
                or type(count) is not int or count<=0 or count>=2**64):
                raise ValueError('invalid sparse count')
            seen.add(index);total+=weights[index]*count;nonzero+=1
    if total>=2**64 or total!=report.get('work_units') or nonzero!=report.get('nonzero_blocks'):
        raise ValueError('weighted vector mismatch')

def arms_for(language):
    if language == 'python': return ('source_plain', 'ir_count')
    if language == 'pypy': return ('direct', 'jit_mapped')
    raise ValueError('unsupported runtime')

def valid_execution(row):
    return (row.get('exit_code') == 0 and row.get('timed_out') is False
        and row.get('stdout_matches_tokens') is True and not row.get('output_truncated')
        and not row.get('descendants_reaped'))

def valid_report(row):
    m = row.get('metric') or {}
    return (valid_execution(row) and not row.get('metric_error')
        and m.get('schema') == 1 and m.get('pid') == row.get('pid')
        and m.get('valid') is True)

def summarize_metric(report, arm, pid):
    """Adapters are finalized with each runtime before the source snapshot freezes."""
    if not isinstance(report, dict) or report.get('schema') != 1 or report.get('pid') != pid:
        raise ValueError('wrong report identity/schema')
    if arm in ('source_plain', 'ir_count'):
        keys = ('schema','pid','valid','metric','work_units','reason','flags','modules_seen',
                'run_id','nonzero_blocks','scope','gaps','authority','validity_scope','model_version')
        result = {key:report[key] for key in keys if key in report}
        if (report.get('authority') != 'diagnostic_only' or report.get('validity_scope') != 'counter_integrity_only' or report.get('model_version') != 'llvm-ir-v1'):
            raise ValueError('unsupported native authority or model')
        if report.get('metric') != 'llvm_ir_block_work': raise ValueError('wrong native metric')
        if report.get('valid') is not True:
            return result
        work = report.get('work_units')
        if type(work) is not int or work < 0: raise ValueError('invalid work count')
        if arm == 'ir_count' and work == 0: raise ValueError('native counter missing')
        if arm == 'source_plain' and work != 0: raise ValueError('plain build unexpectedly metered')
        if report.get('flags')!=0:raise ValueError('native validity disagrees with flags')
        verify_native_vector(report)
        result['vector_verified']=True
        result['metadata_manifest_sha256']=metadata_manifest()[1]
        return result
    from pypy.adapter import summarize
    return summarize(report, arm)
