"""Check seed control, preserved isolation and CLI behavior before the cohort.

Optional argv: one interpreter executable and an output JSON path, for the
small local C-API gate. Without argv check both built experiment interpreters.
"""
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path('/opt/interpreter-meter/cpython')
PROBE = '''import json,sys
print(json.dumps({"hashes":[hash(s) for s in ("n","m","i","j","range","input","print","aonohako-hashseed-v1")],"flags":{k:getattr(sys.flags,k) for k in ("isolated","ignore_environment","no_user_site","no_site","safe_path","hash_randomization")},"argv":sys.argv,"path":sys.path},sort_keys=True))'''
FLAGS = dict(isolated=1, ignore_environment=1, no_user_site=1, no_site=1,
             safe_path=True, hash_randomization=1)


def run(executable, seed, args, python_seed='bad-ignored-value'):
    env = {k:v for k,v in os.environ.items()
           if k not in ('HM_SEED','WM_REPORT_PATH','WM_EXPECT_METER','WM_RUN_ID')}
    if seed is not None:
        env['HM_SEED'] = str(seed)
    env['PYTHONHASHSEED'] = python_seed
    env['PYTHONPATH'] = '/__hashseed_smoke_must_not_be_imported__'
    return subprocess.run([str(executable), *args], env=env,
                          capture_output=True, text=True, timeout=30)


def probe(executable, seed, isolated=True, python_seed='bad-ignored-value'):
    args = ['-I','-S'] if isolated else ['-S']
    result = run(executable, seed, [*args,'-c',PROBE],python_seed)
    assert result.returncode == 0, (executable,seed,result.stderr[-2000:])
    record = json.loads(result.stdout)
    if isolated:
        expected = dict(FLAGS, hash_randomization=int(seed != 0))
        assert record['flags'] == expected, (seed,record)
        assert '/__hashseed_smoke_must_not_be_imported__' not in record['path']
        assert '' not in record['path']
    return record


def check(executable):
    fixed = {}
    for seed in (1, 2, 3, 7, 17, 42, 101, 257, 65537, 123456789, 20261109, 4294967295):
        values = [probe(executable,seed) for _ in range(3)]
        assert all(row == values[0] for row in values), (seed,values)
        reference = probe(executable,None,isolated=False,python_seed=str(seed))
        assert values[0]['hashes'] == reference['hashes'], (seed,values,reference)
        # Even a conflicting standard environment seed is ignored under -I.
        conflict = probe(executable,seed,python_seed=str(seed ^ 1))
        assert conflict['hashes'] == values[0]['hashes']
        fixed[str(seed)] = values[0]
    assert len({tuple(row['hashes']) for row in fixed.values()}) == len(fixed)
    randoms = [probe(executable,'random') for _ in range(4)]
    defaults = [probe(executable,None) for _ in range(4)]
    assert len({tuple(row['hashes']) for row in randoms}) == 4
    assert len({tuple(row['hashes']) for row in defaults}) == 4
    assert all(row['flags'] == FLAGS for row in randoms + defaults)
    zero = probe(executable,0)
    assert zero['hashes'] == probe(executable,None,False,'0')['hashes']
    cli = []
    for args in (['-I','-S','-c','raise SystemExit(7)'],
                 ['-I','-S','--invalid-hashseed-smoke-option'],
                 ['-I','-S','-c','this is invalid syntax'],
                 ['-I','-S','-h'],['--version']):
        default=run(executable,None,args)
        for seed in ('random',7):
            changed=run(executable,seed,args)
            assert changed.returncode == default.returncode,(args,seed,changed)
            assert changed.stdout == default.stdout,(args,seed,changed)
            assert changed.stderr == default.stderr,(args,seed,changed)
        cli.append({'argv':args,'exit_code':default.returncode})
    for invalid in ('','-1','+1','1 ',' 1','randomx','4294967296','999999999999999999999'):
        rejected=run(executable,invalid,['-I','-S','-c','print("executed")'])
        assert rejected.returncode == 2 and not rejected.stdout
        assert rejected.stderr == 'HM_SEED must be random or a decimal uint32\n'
    return {'executable':str(executable),'fixed':fixed,'random':randoms,
            'default':defaults,'zero':zero,'cli':cli,'invalid_seed_tests':8}


def main():
    executables = ([Path(sys.argv[1])] if len(sys.argv)>1 else
                   [ROOT/variant/'bin/python3.13' for variant in ('plain','meter')])
    output = Path(sys.argv[2]) if len(sys.argv)>2 else ROOT/'provenance/hashseed-smoke.json'
    rows=[check(executable) for executable in executables]
    if len(rows)==2:
        for seed in rows[0]['fixed']:
            assert rows[0]['fixed'][seed]['hashes'] == rows[1]['fixed'][seed]['hashes']
    payload={'exit_code':0,'seed_interface':'HM_SEED random or uint32, unset uses upstream Py_BytesMain',
             'rows':rows,'sources':[
                 'https://docs.python.org/3.13/c-api/init_config.html',
                 'https://raw.githubusercontent.com/python/cpython/v3.13.5/Programs/python.c',
                 'https://raw.githubusercontent.com/python/cpython/v3.13.5/Modules/main.c']}
    output.write_text(json.dumps(payload,indent=2,sort_keys=True)+'\n')
    print(json.dumps({'hashseed_smoke':'passed','interpreters':len(rows)}))


if __name__=='__main__':
    main()
