"""Trusted-fixture diagnostic runner. No score is authoritative time or a verdict.

Fresh target processes; blocking wait4, bounded wall safety timer, group reaping.
The launcher is a diagnostic constraint, not the production isolation backend.
"""
import ctypes
import hashlib
import json
import os
import platform
import random
import resource
import shutil
import signal
import subprocess
import tempfile
import time
import uuid
import multiprocessing
from pathlib import Path

ROOT = Path(os.getenv('WORKMETER_ROOT', '/opt/interpreter-meter'))
FIXTURES = ROOT/'fixtures'
MANIFEST = json.loads((FIXTURES/'manifest.json').read_text())
WORKLOADS = {w['id']: w for w in MANIFEST['workloads']}
CAPTURE_LIMIT = 8 << 20
if any(w['expected_bytes'] >= CAPTURE_LIMIT for w in WORKLOADS.values()):
    raise RuntimeError('fixture output exceeds capture budget')
INSTANCE = str(uuid.uuid4())
CPY_PLAIN = ROOT/'cpython/plain/bin/python3.13'
CPY_METER = ROOT/'cpython/meter/bin/python3.13'
from protocol import arms_for
import gzip, base64
RUNTIMES = {}
for name in ('python3', 'pypy3', str(CPY_PLAIN), str(CPY_METER)):
    executable = shutil.which(name)
    if executable:
        version = subprocess.run([executable, '--version'], capture_output=True, timeout=10)
        RUNTIMES[name] = {'path': executable,
            'version': (version.stdout+version.stderr).decode(errors='replace').strip(),
            'executable_sha256': hashlib.sha256(Path(executable).read_bytes()).hexdigest()}
if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
    raise OSError(ctypes.get_errno(), 'subreaper')

def digest(data): return hashlib.sha256(data).hexdigest()
def environment():
    cpu = Path('/proc/cpuinfo').read_text()
    return {'instance_id': INSTANCE, 'revision': os.getenv('K_REVISION'),
        'kernel': platform.release(), 'machine': platform.machine(),
        'cpu_model': next((line.split(':', 1)[1].strip() for line in cpu.splitlines() if line.startswith('model name')), None),
        'fixture_manifest_sha256': digest((FIXTURES/'manifest.json').read_bytes()),
        'runtime_manifest_sha256': digest((ROOT/'runtime-manifest.json').read_bytes()),
        'pypy_runner_sha256': digest((ROOT/'pypy/runner.py').read_bytes()),
        'runtimes': RUNTIMES}

def path(relative):
    p = (FIXTURES/relative).resolve()
    if not p.is_relative_to(FIXTURES): raise ValueError('fixture path escape')
    return p

def run(workload, arm, copy_index=0):
    argv = list(workload['argv'])
    cwd = path(workload['cwd'])
    argv[workload['primary_argv_index']] = str(path(workload['primary']))
    if argv[0] in ('python3', 'pypy3'):
        argv[0] = shutil.which(argv[0])
        if not argv[0]: raise RuntimeError('runtime unavailable')
    elif not argv[0].startswith('/'):
        argv[0] = str((cwd/argv[0]).resolve())
    with tempfile.TemporaryDirectory(prefix='drmeter-') as tmp:
        tmp = Path(tmp)
        os.chown(tmp, 65532, 65532); tmp.chmod(0o700)
        report = tmp/'count.json'
        jitlog = tmp/'jit-counts.log'
        attest_path = tmp/'attestation.json'
        command = list(argv)
        env = {k: v for k, v in os.environ.items() if k in ('PATH', 'LANG', 'LC_ALL')}
        env.update(HOME=str(tmp), TMPDIR=str(tmp), PYTHONDONTWRITEBYTECODE='1')
        if arm == 'source_plain':
            command[0] = str(CPY_PLAIN)
            env.update(WM_REPORT_PATH=str(report), WM_EXPECT_METER='0', HM_SEED='1')
        elif arm == 'ir_count':
            command[0] = str(CPY_METER)
            env.update(WM_REPORT_PATH=str(report), WM_EXPECT_METER='1', HM_SEED='1')
        elif arm in ('jit_count', 'jit_mapped'):
            command = [shutil.which('pypy3'), '-I', '-S', str(ROOT/'pypy/runner.py'),
                       '--mode', 'counts' if arm == 'jit_count' else 'mapped',
                       '--report', str(report), '--', str(path(workload['primary']))]
            env['PYPYLOG'] = 'jit-backend-counts:'+str(jitlog)
        elif arm != 'direct': raise ValueError('unknown arm')
        attest_required = workload['language']=='python' and workload.get('family')!='boundary'
        if attest_required:
            command[command.index('-c')+1] += "\nimport json\nwith open(os.environ['WM_ATTEST_PATH'],'x') as f: json.dump({'pid':os.getpid(),'hashes':[hash(x) for x in ('n','m','i','j','range','input','print','aonohako-hashseed-v1')],'isolated':sys.flags.isolated,'ignore_environment':sys.flags.ignore_environment,'no_site':sys.flags.no_site,'safe_path':sys.flags.safe_path},f)\n"
            env['WM_ATTEST_PATH'] = str(attest_path)
        command = [str(ROOT/'launch'), '--cpu-seconds', str(workload.get('cpu_limit_seconds',20)), *command]
        # Fixed seed is selected by experimental CPython initialization, preserving -I.
        timed_out = False
        def alarm(_signum, _frame):
            nonlocal timed_out
            timed_out = True
            try: os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError: pass
        with path(workload['input']).open('rb') as stdin, tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            start = time.monotonic_ns()
            child = subprocess.Popen(command, cwd=cwd, env=env, stdin=stdin,
                stdout=stdout, stderr=stderr, start_new_session=True)
            previous = signal.signal(signal.SIGALRM, alarm)
            signal.setitimer(signal.ITIMER_REAL, workload.get('wall_timeout_seconds',60))
            descendants = []
            try:
                _, status, usage = os.wait4(child.pid, 0)
                child.returncode = os.waitstatus_to_exitcode(status)
                end = time.monotonic_ns()
            finally:
                signal.setitimer(signal.ITIMER_REAL, 0)
                signal.signal(signal.SIGALRM, previous)
                try: os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError: pass
                deadline = time.monotonic()+5
                while True:
                    try: pid, descendant_status = os.waitpid(-child.pid, os.WNOHANG)
                    except ChildProcessError: break
                    if pid:
                        if pid == child.pid: child.returncode = os.waitstatus_to_exitcode(descendant_status)
                        else: descendants.append({'pid': pid, 'status': descendant_status})
                    elif time.monotonic() > deadline: raise RuntimeError('descendant reap timeout')
                    else: time.sleep(.01)
            stdout.seek(0); stderr.seek(0)
            output = stdout.read(CAPTURE_LIMIT); errors = stderr.read(CAPTURE_LIMIT)
            truncated = os.fstat(stdout.fileno()).st_size > len(output) or os.fstat(stderr.fileno()).st_size > len(errors)
        expected = path(workload['expected']).read_bytes()
        metric = None
        metric_error = None
        raw_report = None
        if report.exists():
            try:
                if report.stat().st_size > (64 << 20): raise ValueError('oversized count report')
                report_bytes = report.read_bytes()
                raw_report = {'encoding':'gzip+base64', 'sha256':digest(report_bytes),
                              'bytes':len(report_bytes),
                              'data':base64.b64encode(gzip.compress(report_bytes, mtime=0)).decode()}
                parsed = json.loads(report_bytes)
                from protocol import summarize_metric
                metric = summarize_metric(parsed, arm, child.pid)
            except (OSError, ValueError, TypeError, KeyError) as error:
                metric_error = type(error).__name__+': '+str(error)[:200]
        raw_jitlog = None
        if jitlog.exists():
            if jitlog.stat().st_size > (16 << 20):
                metric_error = 'oversized JIT log'
            else:
                log_bytes = jitlog.read_bytes()
                raw_jitlog = {'encoding':'gzip+base64', 'sha256':digest(log_bytes),
                             'bytes':len(log_bytes),
                             'data':base64.b64encode(gzip.compress(log_bytes, mtime=0)).decode()}
        attestation = None
        attestation_error = None
        if attest_required:
            try:
                if attest_path.stat().st_size>4096: raise ValueError('oversized attestation')
                attestation=json.loads(attest_path.read_bytes())
                expected_hashes=json.loads((ROOT/'cpython/provenance/hashseed-smoke.json').read_text())['rows'][0]['fixed']['1']['hashes']
                if attestation!={'pid':child.pid,'hashes':expected_hashes,'isolated':1,'ignore_environment':1,'no_site':1,'safe_path':True}:
                    raise ValueError('hash seed or isolation differs')
            except (ValueError,OSError,KeyError,TypeError) as error:
                attestation_error=str(error)[:200]
        termination = 'wall_limit'  if timed_out else 'cpu_limit' if child.returncode == -signal.SIGXCPU else 'signal' if child.returncode < 0 else 'exit'
        return {'attestation':attestation,'attestation_error':attestation_error,'termination':termination, 'copy_index':copy_index, 'authoritative_time':False, 'workload_id': workload['id'], 'submission_id': workload['submission_id'],
            'language': workload['language'], 'kind':workload.get('kind','submission'),
            'family':workload.get('family'), 'scale':workload.get('scale'), 'arm': arm, 'exit_code': child.returncode, 'pid': child.pid,
            'timed_out': timed_out, 'wall_ms': (end-start)/1e6,
            'cpu_ms': (usage.ru_utime+usage.ru_stime)*1000,
            'user_cpu_ms': usage.ru_utime*1000, 'system_cpu_ms': usage.ru_stime*1000,
            'max_rss_kb': usage.ru_maxrss, 'stdout_sha256': digest(output),
            'stdout_bytes': len(output), 'stdout_matches_tokens': output.split()==expected.split(),
            'stderr_excerpt': errors[:4096].decode(errors='replace'),
            'stderr_bytes': len(errors), 'stderr_sha256': digest(errors),
            'stderr_excerpt_truncated': len(errors)>4096, 'output_truncated': truncated,
            'metric': metric, 'metric_error': metric_error, 'raw_report':raw_report, 'raw_jitlog':raw_jitlog, 'descendants_reaped': descendants,
            'boundary': 'wait4 of launcher/runtime/application/report/teardown; overhead inclusive',
            'source_argv': argv, 'executed_runtime':command[3],
            'meter_mode': {'WM_EXPECT_METER':env.get('WM_EXPECT_METER'),
                           'hash_seed':env.get('HM_SEED'),
                           'pypy_backend_counts':arm in ('jit_count','jit_mapped')}}

def execute(workload_id, iteration, parallelism, order_seed, selected_arms=None):
    workload = WORKLOADS[workload_id]
    if parallelism not in (1, 2): raise ValueError('parallelism must be 1 or 2')
    arms = list(selected_arms or arms_for(workload['language']))
    if not set(arms).issubset(arms_for(workload['language'])): raise ValueError('unsupported arm')
    random.Random(order_seed).shuffle(arms)
    rows = []; batches = []
    request_start = time.monotonic_ns()
    with multiprocessing.get_context('spawn').Pool(parallelism) as pool:
        for arm in arms:
            start = time.monotonic_ns()
            results = pool.starmap(run, [(workload, arm, i) for i in range(parallelism)])
            batches.append({'arm': arm, 'wall_ms': (time.monotonic_ns()-start)/1e6,
                            'sum_target_cpu_ms': sum(r['cpu_ms'] for r in results)})
            rows.extend(results)
    return {'environment': environment(), 'workload_id': workload_id, 'iteration': iteration,
            'parallelism': parallelism, 'order': arms, 'rows': rows, 'batches': batches,
            'request_wall_ms': (time.monotonic_ns()-request_start)/1e6,
            'parallel_scope': 'single target' if parallelism==1 else 'same-instance interference stress, not production TC dispatch'}

if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workload', required=True)
    parser.add_argument('--parallelism', type=int, choices=(1,2), default=1)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    with args.output.open('x') as out:
        json.dump(execute(args.workload, 0, args.parallelism, 'local'), out, allow_nan=False)
