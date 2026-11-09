"""Run the full immutable corpus once before temporary Cloud Run deployment."""
import json
import os
from pathlib import Path
import subprocess
import traceback
import runner
from protocol import arms_for
from validate import check_batch

def main():
    os.umask(0o077)
    out=Path('/output');out.mkdir(exist_ok=True)
    status={'exit_code':1,'batches':0,'failures':[]}
    try:
        first=next(iter(runner.WORKLOADS.values()))
        check='''import os,socket,sys
assert os.getuid()==65532
try: socket.socket()
except PermissionError: pass
else: raise RuntimeError('network available')
try: os.open(sys.argv[1],os.O_WRONLY)
except PermissionError: pass
else: raise RuntimeError('fixture writable')
'''
        subprocess.run([str(runner.ROOT/'launch'),'/usr/bin/python3','-I','-S','-c',check,str(runner.path(first['primary']))],check=True,timeout=30)
        with (out/'local-responses.ndjson').open('w') as f:
            for w in runner.WORKLOADS.values():
                req={'workload_id':w['id'],'iteration':0,'parallelism':1,'order_seed':'preflight','arms':list(arms_for(w['language']))}
                result=runner.execute(w['id'],0,1,'preflight',req['arms'])
                result.update(request=req,service='preflight')
                f.write(json.dumps(result,allow_nan=False)+'\n');f.flush();status['batches']+=1
                errors=check_batch(result,w,req)
                if errors: status['failures'].append({'workload':w['id'],'errors':errors})
        # One real pool with two target processes exercises wait4 ownership.
        req=dict(workload_id=first['id'],iteration=0,parallelism=2,order_seed='preflight-stress',arms=list(arms_for(first['language'])))
        result=runner.execute(first['id'],0,2,'preflight-stress',req['arms'])
        result.update(request=req,service='preflight-stress')
        with (out/'local-stress.json').open('w') as f: json.dump(result,f,allow_nan=False)
        errors=check_batch(result,first,req)
        if errors:status['failures'].append({'workload':first['id'],'parallelism':2,'errors':errors})
        status['exit_code']=int(bool(status['failures']))
    except BaseException as e:
        status['failures'].append({'type':type(e).__name__,'detail':str(e),'traceback':traceback.format_exc(limit=5)})
    (out/'preflight.json').write_text(json.dumps(status,indent=2)+'\n')
    print(json.dumps({k:v for k,v in status.items() if k!='failures'}))

if __name__=='__main__':main()
