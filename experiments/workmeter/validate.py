"""Separate expected execution outcomes from counter availability and coverage."""
from protocol import arms_for

def check_row(row, workload):
    errors=[]
    if row.get('workload_id')!=workload['id']: errors.append('wrong_workload')
    expected=workload.get('expected_execution',{'kind':'exit','code':0})
    if row.get('termination')!=expected['kind']: errors.append('wrong_termination')
    if expected['kind']=='exit' and row.get('exit_code')!=expected['code']: errors.append('wrong_exit_code')
    if row.get('stdout_matches_tokens') is not True: errors.append('wrong_output')
    if row.get('output_truncated'): errors.append('truncated_output')
    if row.get('descendants_reaped'): errors.append('unreaped_descendant_at_target_exit')
    if row.get('metric_error'): errors.append('invalid_report_format')
    if workload['language']=='python' and workload.get('family')!='boundary' and (not row.get('attestation') or row.get('attestation_error')): errors.append('missing_seed_attestation')
    arm=row['arm'];metric=row.get('metric')
    if arm in ('ir_count','source_plain'):
        policy=workload.get('expected_metric',{}).get('ir_count','valid')
        if policy=='missing':
            if metric is not None or row.get('raw_report') is not None: errors.append('unexpected_report')
        elif policy=='unsupported_threads':
            if not metric or metric.get('valid') is not False or not metric.get('flags',0)&8 or metric.get('work_units') is not None:
                errors.append('unsupported_threads_not_marked_invalid')
        elif not metric or metric.get('valid') is not True:
            errors.append('missing_or_invalid_native_report')
        if metric and metric.get('pid')!=row.get('pid'): errors.append('report_pid_mismatch')
    elif arm=='jit_mapped':
        if metric and metric.get('pid')!=row.get('pid'): errors.append('report_pid_mismatch')
        if metric and metric.get('full_program_coverage') is not False: errors.append('jit_coverage_claim')
    elif arm!='direct': errors.append('unknown_arm')
    if row.get('authoritative_time') is not False: errors.append('authoritative_time_claim')
    return errors

def check_batch(result, workload, request, instance=None):
    errors=[]
    for key in ('workload_id','iteration','parallelism'):
        if result.get(key)!=request[key]: errors.append('wrong_'+key)
    rows=result.get('rows',[])
    expected={(a,i) for a in request['arms'] for i in range(request['parallelism'])}
    actual=[(r.get('arm'),r.get('copy_index')) for r in rows]
    if len(actual)!=len(set(actual)) or set(actual)!=expected: errors.append('missing_or_duplicate_rows')
    if not set(request['arms']).issubset(arms_for(workload['language'])): errors.append('unsupported_arms')
    current=result.get('environment',{}).get('instance_id')
    if not current or (instance is not None and current!=instance): errors.append('instance_changed')
    for row in rows: errors.extend(row['arm']+':'+error for error in check_row(row,workload))
    return errors
