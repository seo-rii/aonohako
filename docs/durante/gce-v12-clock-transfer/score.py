"""Replay a frozen Aonohako v12 transfer study from the original HTTP receipts."""
import argparse
from collections import Counter, defaultdict
from fractions import Fraction
import gzip
import hashlib
import guest
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


def load_samples(root, plan, cases, files, payload):
    traces = read_placement(root)
    require(len(traces) == len(plan['schedule']), 'missing trace windows')
    digests = {key: files[path] for key, path in (
        ('launcher_sha256', 'durante/dynamorio/bin64/drrun'),
        ('client_sha256', 'durante/lib/libdurante.so'),
        ('release_core_sha256', 'durante/dynamorio/lib64/release/libdynamorio.so'))}
    rows = defaultdict(lambda: defaultdict(list))
    seen = set()
    request_hashes = {}
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
            arm = 'native' if kind == 'warmup' else kind
            key = (case_id, arm)
            if key not in request_hashes:
                if payload is None:
                    request_hashes[key] = r['request_sha256']
                else:
                    body = guest.make_body(c, arm, payload)
                    if 'capture_stdout_bytes' in plan:
                        from scale.guest import with_capture
                        body = with_capture(body)
                    request_hashes[key] = hashlib.sha256(body).hexdigest()
            require(re.fullmatch(r'[a-f0-9]{64}', r['request_sha256']) and
                    r['request_sha256'] == request_hashes[key] and r['service_port'] == guest.PORTS[arm],
                    f'HTTP request differs at {seq}')
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
                cpu = result['cpu_accounting']
                require(cpu['wait_target_ns'] == cpu['wait_user_ns'] + cpu['wait_system_ns'] - cpu['rusage_baseline_ns'],
                        f'wait accounting at {seq}')
                source = cpu['selected_source']
                if source in ('process_clock', 'wait_rusage'):
                    selected = cpu['process_clock_peak_ns'] if source == 'process_clock' else cpu['wait_target_ns']
                    require(selected == result['cpu_time_ns'] == max(cpu['process_clock_peak_ns'], cpu['wait_target_ns']),
                            f'selected CPU time at {seq}')
                else:
                    require(source == 'cgroup' and result['cpu_time_ns'] >= max(cpu['process_clock_peak_ns'], cpu['wait_target_ns']),
                            f'unknown CPU accounting at {seq}')
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


def pair_bound(rows, ids, mode='live', fields=('instructions', 'movs_bytes', 'stos_bytes')):
    best = None
    best_error = Fraction(0)
    for slow in ids:
        for fast in ids:
            native_ratio = Fraction(min(rows[slow]['native']), max(rows[fast]['native']))
            ratios = []
            for field in fields:
                upper = max(r[field] for r in rows[slow][mode])
                lower = min(r[field] for r in rows[fast][mode])
                if upper and not lower:
                    break
                if lower:
                    ratios.append(Fraction(upper, lower))
            else:
                counter_ratio = max(ratios)
                error = (native_ratio - counter_ratio) / (native_ratio + counter_ratio)
                if error > best_error:
                    best_error = error
                    # Ceil can add less than one nanosecond to the slow prediction.
                    slow_ns = min(rows[slow]['native'])
                    fast_ns = max(rows[fast]['native'])
                    rounded_bound = max(Fraction(0), (slow_ns - counter_ratio * fast_ns - 1) /
                                        (slow_ns + counter_ratio * fast_ns))
                    best = dict(slow=slow, fast=fast, native_ratio=str(native_ratio),
                                max_counter_ratio=str(counter_ratio), lower_bound_ppm=ppm(error),
                                ceil_lower_bound_ppm_floor=math.floor(rounded_bound * MILLION))
    return best


def scale_live_bound(rows):
    # These three archived readings certify a lower bound regardless of the weights.
    money = rows['money/terraced_bids']
    scrivener = rows['scrivener/interwoven_pages']
    wall = rows['wall/phase_envelopes']
    m, s, w = money['live'][0], scrivener['live'][0], wall['live'][1]

    def det(a, b):
        return a['instructions'] * b['stos_bytes'] - a['stos_bytes'] * b['instructions']

    p = Fraction(det(m, w), det(s, w))
    q = Fraction(det(s, m), det(s, w))
    require(p > 0 and q > 0 and all(m[k] == p * s[k] + q * w[k]
            for k in ('instructions', 'movs_bytes', 'stos_bytes')), 'live witness identity')

    def error_bound(money_ns, scrivener_ns, wall_ns, rounding_slack):
        weighted_ns = p * scrivener_ns + q * wall_ns
        return (weighted_ns - money_ns - rounding_slack) / (weighted_ns + money_ns)

    ym, ys, yw = (median(r['native']) for r in (money, scrivener, wall))
    e = error_bound(ym, ys, yw, 0)
    # Interpolate estimates at the two lower faces of the minimax interval.
    instruction_weight = (1 - e) * (ys * w['stos_bytes'] - yw * s['stos_bytes']) / det(s, w)
    stos_weight = (1 - e) * (s['instructions'] * yw - w['instructions'] * ys) / det(s, w)
    require(min(instruction_weight, stos_weight) >= 0, 'negative witness weight')
    worst = max(abs((instruction_weight * r['instructions'] + stos_weight * r['stos_bytes'])
                    / median(case['native']) - 1) for case in rows.values() for r in case['live'])
    require(worst == e, 'median lower bound not attained across the panel')
    # Each ceil changes a prediction by less than 1 ns. Allow p+q ns on the two RHS terms.
    conservative = error_bound(max(money['native']), min(scrivener['native']),
                               min(wall['native']), p + q)
    return dict(scrivener_multiplier=str(p), wall_multiplier=str(q),
                median_continuous_minimax=str(e),
                median_ceil_lower_bound_ppm_floor=math.floor(error_bound(ym, ys, yw, p + q) * MILLION),
                observed_range_ceil_lower_bound_ppm_floor=math.floor(conservative * MILLION))


def evaluate(root, payload=None):
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
        local = root / path if (root / path).is_file() else HERE / path
        require(files[path] == hashlib.sha256(local.read_bytes()).hexdigest(), f'changed frozen {path}')
    if 'capture_stdout_bytes' in plan:
        require(files['base_guest.py'] == hashlib.sha256((HERE / 'guest.py').read_bytes()).hexdigest()
                and plan['capture_stdout_bytes'] == 1 << 20, 'scale guest or capture contract differs')
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
    rows = load_samples(root, plan, cases, files, payload)
    scores = {}
    for name, c in cases.items():
        native = rows[name]['native']
        require(tuple(len(rows[name][k]) for k in ('warmup', 'native', 'live', 'counters')) == (1, 7, 2, 2), name)
        ref = median(native)
        spread = Fraction(max(native) - min(native), ref)
        item = dict(group=c['group'], program=c['program'], native_ns=native, minimum_ns=min(native), median_ns=ref, maximum_ns=max(native),
                    native_spread_ppm=ppm(spread), stable=ref >= 20 * MILLION and spread <= Fraction(1, 10),
                    component_deltas={}, models={})
        for mode in ('live', 'counters'):
            a, b = rows[name][mode]
            item['component_deltas'][mode] = {k: abs(v - b[k]) for k, v in a.items()}
        for model, spec in plan['models'].items():
            weights = {k: Fraction(v) for k, v in spec['weights_ns'].items()}
            pred = [math.ceil(sum(w * r[k] for k, w in weights.items())) for r in rows[name][spec['kind']]]
            errors = [Fraction(v - ref, ref) for v in pred]
            low = 4 * ref // (5 * MILLION)
            high = math.ceil(Fraction(6 * ref, 5 * MILLION))
            item['models'][model] = dict(predicted_ns=pred, errors_ppm=[ppm(e) for e in errors],
                within_20pct=all(abs(e) <= Fraction(1, 5) for e in errors),
                boundaries=[classify(native, pred, ms) for ms in (low, high)])
        scores[name] = item
    groups = {}
    for group in plan['groups']:
        subset = [s for s in scores.values() if s['group'] == group]
        g = dict(cases=len(subset), stable=sum(s['stable'] for s in subset), models={})
        for model in plan['models']:
            results = [s['models'][model] for s in subset]
            votes = Counter(b['outcome'] for r in results for b in r['boundaries'])
            worst = [max(map(abs, r['errors_ppm'])) for r in results]
            stable_results = [s['models'][model] for s in subset if s['stable']]
            g['models'][model] = dict(within_20pct=sum(r['within_20pct'] for r in results),
                stable_within_20pct=sum(r['within_20pct'] for r in stable_results),
                median_case_max_abs_error_ppm=round(median(worst)), worst_abs_error_ppm=max(worst),
                boundaries={k: votes[k] for k in ('agree', 'false_tle', 'false_pass', 'mixed_native')})
        groups[group] = g
    holdout = 'scale_holdout' if 'scale_holdout' in groups else 'contest_holdout'
    ids = [k for k, v in cases.items() if v['group'] == holdout]
    result = dict(groups=groups, cases=scores, **{holdout.split('_')[0] + '_pair_bound': pair_bound(rows, ids)})
    if holdout == 'scale_holdout':
        result['retrospective_bounds'] = dict(
            instructions_movs_stos=scale_live_bound(rows),
            latency_operand_bytes=pair_bound(rows, ids, 'counters', ('cycles_x16', 'memory_operand_bytes')))
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=HERE)
    parser.add_argument('--payload', type=Path, help='rebuild the exact request digests from a prepared corpus')
    parser.add_argument('--write', action='store_true', help='write the derived summary')
    args = parser.parse_args()
    result = evaluate(args.root, args.payload)
    path = args.root / 'results.json'
    if args.write:
        path.write_text(json.dumps(result, indent=2) + '\n')
    else:
        require(json.loads(path.read_text()) == result, 'results differ from the raw receipts')
        print('Verified every scheduled execution and frozen model comparison.')
