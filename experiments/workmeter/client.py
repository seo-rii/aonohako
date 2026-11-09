"""Collect a predeclared corpus from already-created private diagnostic services.

This command creates no infrastructure. For Cloud Run pass the name of an
environment variable containing an identity token; the token is never printed.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import random
import time
import urllib.parse
import urllib.request
from protocol import arms_for
from validate import check_batch

def requests(workloads, service, repeats):
    result=[]
    for w in workloads:
        levels=(1,2) if w.get('kind')=='submission' or w.get('scale')==4 else (1,)
        for parallelism in levels:
            for i in range(repeats):
                result.append({'workload_id':w['id'],'parallelism':parallelism,'iteration':i,
                    'arms':list(arms_for(w['language'])),'order_seed':service+':'+w['id']+':'+str(i)})
    random.Random(service+':workmeter-v1').shuffle(result)
    return result

def collect(root, service, url, workloads, repeats, token):
    output=root/(service+'-responses.ndjson')
    by_id={w['id']:w for w in workloads};failures=[];instance=None
    with output.open('x') as f:
        for request in requests(workloads,service,repeats):
            started=time.monotonic_ns()
            try:
                headers={'Content-Type':'application/json'}
                if token:headers['Authorization']='Bearer '+token
                req=urllib.request.Request(url.rstrip('/')+'/run',data=json.dumps(request).encode(),headers=headers,method='POST')
                with urllib.request.urlopen(req,timeout=450) as response:
                    raw=response.read((32<<20)+1)
                    if len(raw)>32<<20:raise ValueError('response too large')
                    result=json.loads(raw)
                    if not isinstance(result,dict):raise ValueError('response must be an object')
            except Exception as e:
                result={'request_error':type(e).__name__}
            result.update(service=service,request=request,client_wall_ms=(time.monotonic_ns()-started)/1e6)
            f.write(json.dumps(result,allow_nan=False)+'\n');f.flush()
            try:errors=check_batch(result,by_id[request['workload_id']],request,instance)
            except (KeyError,ValueError,TypeError,AttributeError) as e:errors=['response_contract:'+type(e).__name__]
            if errors:failures.append({'request':request,'errors':errors})
            current=result.get('environment',{}).get('instance_id')
            if instance is None and current:instance=current
    return {'service':service,'instance':instance,'failures':failures}

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=Path,required=True)
    p.add_argument('--url',action='append',required=True)
    p.add_argument('--identity-token-env')
    p.add_argument('--repeats',type=int,choices=range(1,10),default=3)
    args=p.parse_args();os.umask(0o077)
    if not 1<=len(args.url)<=8:p.error('use between one and eight service URLs')
    for url in args.url:
        parsed=urllib.parse.urlsplit(url)
        if parsed.scheme!='https' and not (parsed.scheme=='http' and parsed.hostname in ('127.0.0.1','localhost','::1')):
            p.error('use HTTPS or loopback HTTP')
        if parsed.username or parsed.password or parsed.query or parsed.fragment:p.error('URL must not contain credentials/query/fragment')
    token=os.environ[args.identity_token_env] if args.identity_token_env else None
    config={'prefix':'workmeter','services':['workmeter-'+str(i+1).zfill(2) for i in range(len(args.url))]}
    workloads=json.loads((args.root/'fixtures/manifest.json').read_bytes())['workloads']
    if any(w.get('kind')=='submission' for w in workloads):
        p.error('this public client requires the public corpus; private submission fanout needs a separate approved runner')
    with (args.root/'config.json').open('x') as f:json.dump(config,f,indent=2)
    with ThreadPoolExecutor(max_workers=len(args.url)) as pool:
        futures=[pool.submit(collect,args.root,s,u,workloads,args.repeats,token) for s,u in zip(config['services'],args.url)]
        results=[f.result() for f in futures]
    status={'exit_code':int(any(r['failures'] for r in results)),'results':results}
    with (args.root/'collection-status.json').open('x') as f:json.dump(status,f,indent=2)
    print(json.dumps({'exit_code':status['exit_code'],'services':len(results)}))
    return status['exit_code']

if __name__=='__main__':raise SystemExit(main())
