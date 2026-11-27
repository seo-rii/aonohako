"""Replay a frozen Aonohako v12 transfer study from the original HTTP receipts."""
import argparse
from collections import Counter, defaultdict
from fractions import Fraction
import gzip
import hashlib
import json
import math
from pathlib import Path
import re
from statistics import median

HERE = Path(__file__).resolve().parent
MILLION = 1_000_000


def require(condition, message):
    if not condition:
        raise ValueError(message)


def exact(value):
    return type(value) is int and value >= 0


def ppm(ratio):
    return round(ratio * MILLION)


def read_placement(root):
    traces = []
    with gzip.open(root / 'exec-trace.txt.gz', 'rt') as stream:
        for line in stream:
            if line.startswith('# trial '):
                require(int(line.split()[2]) == len(traces), 'trace sequence mismatch')
                traces.append([])
            elif line.strip():
                event = re.search(r'\[(\d+)\].*?sched_process_exec: filename=(.*?) pid=(\d+) old_pid=', line)
                require(event is not None and traces, 'malformed trace event')
                cpu, path, pid = event.groups()
                traces[-1].append(dict(cpu=int(cpu), path=path, pid=int(pid)))
    return traces


def load_samples(root, plan, cases, files):
    traces = read_placement(root)
    require(len(traces) == len(plan['schedule']), 'missing trace windows')
    digests = {key: files[path] for key, path in (
        ('launcher_sha256', 'durante/dynamorio/bin64/drrun'),
        ('client_sha256', 'durante/lib/libdurante.so'),
        ('release_core_sha256', 'durante/dynamorio/lib64/release/libdynamorio.so'))}
    rows = defaultdict(lambda: defaultdict(list))
    seen = set()
    with gzip.open(root / 'receipts.jsonl.gz', 'rt') as stream:
        for seq, (line, trial) in enumerate(zip(stream, plan['schedule'], strict=True)):
            r = json.loads(line)
            case_id, kind, rep = trial['case_id'], trial['kind'], trial['rep']
            c = cases[case_id]
            key = (case_id, kind, rep)
            require(key not in seen and r['sequence'] == seq and
                    all(r[k] == v for k, v in trial.items()), f'trial mismatch at {seq}')
            seen.add(key)
            require(all(r[k] == c[k] for k in ('program', 'binary_sha256', 'stdin_sha256', 'stdout_sha256')),
                    f'workload identity at {seq}')
            events = r['execs']
            target = 'Main' if kind in ('warmup', 'native') else 'drrun'
            require(events == traces[seq] and sum(Path(e['path']).name == target for e in events) == 1 and
                    all(e['cpu'] == 1 for e in events), f'CPU placement at {seq}')
            result = r['receipt']
            require(result['exit_code'] == 0 and result['cpu_time_ns'] > 0 and
                    not result.get('cpu_time_normalization') and not result.get('cpu_time_sampling'), f'execution at {seq}')
            if kind in ('warmup', 'native'):
                require(result['status'] == 'Accepted' and result['verdict_source'] == 'stdout' and
                        not result.get('work_measurement') and result['cpu_accounting']['selected_source'],
                        f'native result at {seq}')
                rows[case_id][kind].append(result['cpu_time_ns'])
                continue
            m, env = result['work_measurement'], result['work_measurement_execution']
            require(env['state'] == 'complete' and env['output_matches'] and m['complete'] and
                    m['authority'] == 'diagnostic_only' and m['integrity_verified'] is False and
                    m['cpuid_profile'] == 'x86-64-v2' and m['engine_profile'] == 'dynamorio-11.3-durante-v12-vm32m' and
                    m['quality'] == 'uninterrupted' and m['signals_observed'] == m['unsupported_rep_entries'] == 0 and
                    m['rep_telemetry_status'] == 'movs_stos_only' and m['processes'] == m['images'] == 1,
                    f'ineligible counters at {seq}')
            installation = m['installation']
            require(installation['source_commit'] == plan['durante_commit'] and installation['unchanged_before_after']
                    and all(installation[k] == digest for k, digest in digests.items()), f'engine identity at {seq}')
            b = m['rep_breakdown']
            counts = dict(instructions=m['instructions'], rep_bytes=m['rep_bytes'], movs_bytes=b['movs_bytes'],
                          stos_bytes=b['stos_bytes'])
            require(m['instructions'] > 0 and all(exact(v) for v in counts.values()) and
                    m['rep_bytes'] == b['movs_bytes'] + b['stos_bytes'], f'REP counts at {seq}')
            if kind == 'live':
                clock = env['clock']
                require(clock['metric'] == 'instructions_plus_movs_stos_bytes_v1' and clock['state'] == 'observed' and
                        clock['budget'] == (1 << 63) - 1 and
                        all(clock['progress'][k] == counts[k] for k in ('instructions', 'movs_bytes', 'stos_bytes')) and
                        'latency_bound' not in m and 'memory_operand_bytes' not in m, f'live reconciliation at {seq}')
            else:
                require(kind == 'counters' and 'clock' not in env and
                        m['latency_bound']['version'] == m['memory_operand_bytes']['version'] == 1, f'counter mode at {seq}')
                counts.update(cycles_x16=m['latency_bound']['cycles_x16'],
                              memory_operand_bytes=m['memory_operand_bytes']['bytes'])
                require(exact(counts['memory_operand_bytes']) and counts['cycles_x16'] >= 4 * counts['instructions'],
                        f'counter range at {seq}')
            rows[case_id][kind].append(counts)
    require(set(rows) == set(cases), 'incomplete case set')
    return rows


def classify(native, predictions, limit_ms):
    budget = limit_ms * MILLION
    n = sum(t > budget for t in native)
    p = sum(t > budget for t in predictions)
    if 0 < n < len(native):
        outcome = 'mixed_native'
    elif n == 0 and p:
        outcome = 'false_tle'
    elif n == len(native) and p < len(predictions):
        outcome = 'false_pass'
    else:
        outcome = 'agree'
    return dict(limit_ms=limit_ms, native_over=n, predicted_over=p, outcome=outcome)


def pair_bound(rows, ids):
    best = None
    best_error = Fraction(0)
    for slow in ids:
        for fast in ids:
            native_ratio = Fraction(min(rows[slow]['native']), max(rows[fast]['native']))
            ratios = []
            for field in ('instructions', 'movs_bytes', 'stos_bytes'):
                upper = max(r[field] for r in rows[slow]['live'])
                lower = min(r[field] for r in rows[fast]['live'])
                if upper and not lower:
                    break
                if lower:
                    ratios.append(Fraction(upper, lower))
            else:
                counter_ratio = max(ratios)
                error = (native_ratio - counter_ratio) / (native_ratio + counter_ratio)
                if error > best_error:
                    best_error = error
                    best = dict(slow=slow, fast=fast, native_ratio=str(native_ratio),
                                max_counter_ratio=str(counter_ratio), lower_bound_ppm=ppm(error))
    return best


def evaluate(root):
    plan = json.loads((root / 'plan.json').read_bytes())
    index = (root / 'cases.json').read_bytes()
    require(hashlib.sha256(index).hexdigest() == plan['cases_sha256'], 'case manifest changed')
    cases = {r['case_id']: r for r in json.loads(index)}
    require(Counter(c['group'] for c in cases.values()) == plan['groups'], 'group counts')
    files = {}
    for line in (root / 'inputs.sha256').read_text().splitlines():
        digest, name = line.split('  ', 1)
        files[name.removeprefix('./')] = digest
    for path in ('plan.json', 'cases.json', 'guest.py', 'guest.sh'):
        require(files[path] == hashlib.sha256((root / path).read_bytes()).hexdigest(), f'changed frozen {path}')
    for c in cases.values():
        for role in ('binary', 'stdin', 'stdout'):
            require(files[c[role + '_path']] == c[role + '_sha256'], 'case differs from payload')
    placement = json.loads((root / 'platform.json').read_bytes())
    host, gce = placement['guest'], placement['gce']
    require(not host['preflight'] and host['affinity'] == [1] and host['cpuset_cpus_effective'] == '1' and
            host['cpu'] == 1 and host['cpu1_thread_siblings'] == '1' and
            all(cpus == [1] for cpus in host['worker_affinities'].values()) and
            host['plan_sha256'] == files['plan.json'] and host['cases_sha256'] == files['cases.json'] and
            host['steal_ticks_after'] >= host['steal_ticks_before'] and
            host['server_sha256'] == files['server'] and gce['cpu_platform'] == 'AMD Milan' and
            gce['machine_type'] == 'n2d-standard-4' and gce['threads_per_core'] == 1, 'wrong reference platform')
    rows = load_samples(root, plan, cases, files)
    scores = {}
    for name, c in cases.items():
        native = rows[name]['native']
        require(tuple(len(rows[name][k]) for k in ('warmup','native','live','counters')) == (1,7,2,2), name)
        ref = median(native)
        spread = Fraction(max(native) - min(native), ref)
        item = dict(group=c['group'], program=c['program'], native_ns=native, minimum_ns=min(native), median_ns=ref, maximum_ns=max(native),
                    native_spread_ppm=ppm(spread), stable=ref >= 20*MILLION and spread <= Fraction(1,10),
                    component_deltas={}, models={})
        for mode in ('live', 'counters'):
            a, b = rows[name][mode]
            item['component_deltas'][mode] = {k: abs(v - b[k]) for k, v in a.items()}
        for model, spec in plan['models'].items():
            weights = {k: Fraction(v) for k,v in spec['weights_ns'].items()}
            pred = [math.ceil(sum(w * r[k] for k,w in weights.items())) for r in rows[name][spec['kind']]]
            errors = [Fraction(v - ref, ref) for v in pred]
            low = 4*ref // (5*MILLION)
            high = math.ceil(Fraction(6*ref, 5*MILLION))
            item['models'][model] = dict(predicted_ns=pred, errors_ppm=[ppm(e) for e in errors],
                within_20pct=all(abs(e) <= Fraction(1,5) for e in errors),
                boundaries=[classify(native, pred, ms) for ms in (low, high)])
        scores[name] = item
    groups = {}
    for group in plan['groups']:
        subset = [s for s in scores.values() if s['group'] == group]
        g = dict(cases=len(subset), stable=sum(s['stable'] for s in subset), models={})
        for model in plan['models']:
            results = [s['models'][model] for s in subset]
            votes = Counter(b['outcome'] for r in results for b in r['boundaries'])
            worst = [max(map(abs,r['errors_ppm'])) for r in results]
            g['models'][model] = dict(within_20pct=sum(r['within_20pct'] for r in results),
                median_case_max_abs_error_ppm=round(median(worst)), worst_abs_error_ppm=max(worst),
                boundaries={k:votes[k] for k in ('agree','false_tle','false_pass','mixed_native')})
        groups[group] = g
    return dict(groups=groups, cases=scores,
                contest_pair_bound=pair_bound(rows, [k for k,v in cases.items() if v['group'] == 'contest_holdout']))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=HERE)
    parser.add_argument('--write', action='store_true', help='write the derived summary')
    args = parser.parse_args()
    result = evaluate(args.root)
    path = args.root / 'results.json'
    if args.write:
        path.write_text(json.dumps(result, indent=2) + '\n')
    else:
        require(json.loads(path.read_text()) == result, 'results differ from the raw receipts')
        print('Verified all 396 scheduled executions and frozen model comparisons.')
