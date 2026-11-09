import hashlib,json,os,pathlib,subprocess,tempfile,time
ROOT=pathlib.Path('/opt/interpreter-meter/cpython')
metadata={p.stem:json.loads(p.read_text()) for p in (ROOT/'metadata').glob('*.json')}

def invoke(variant,program):
 with tempfile.TemporaryDirectory() as d:
  report=pathlib.Path(d)/'report.json';env=dict(os.environ,WM_REPORT_PATH=str(report),WM_EXPECT_METER=str(int(variant=='meter')),WM_RUN_ID='smoke')
  started=time.monotonic();r=subprocess.run([str(ROOT/variant/'bin/python3.13'),'-I','-S','-c',program],env=env,capture_output=True,timeout=90)
  wall=time.monotonic()-started
  if r.returncode:raise RuntimeError((variant,r.returncode,r.stderr[-2000:].decode(errors='replace')))
  m=json.loads(report.read_text());assert m['schema']==1 and m['valid'] and m['run_id']=='smoke'
  expected=0;nonzero=0
  for module in m['modules']:
   entry=metadata[module['metadata_id']];assert module['blocks']==len(entry['blocks'])
   indexes=set()
   for index,count in module['counts']:
    assert index not in indexes and 0<=index<module['blocks'] and count>0
    indexes.add(index);expected+=entry['blocks'][index][1]*count;nonzero+=1
  assert expected==m['work_units'] and nonzero==m['nonzero_blocks']
  if variant=='meter':assert expected>0
  return {'variant':variant,'output':r.stdout.decode(),'work_units':m['work_units'],'modules_seen':m['modules_seen'],'nonzero_blocks':m['nonzero_blocks'],'wall_seconds':wall,'report_bytes':report.stat().st_size}
programs={}
for n in (100000,200000,400000):programs['loop-'+str(n)]=f'x=0\nfor i in range({n}): x=(x+i)%1000000007\nprint(x)'
for n in (2000,8000):programs['sort-'+str(n)]=f'a=[(i*48271)%100003 for i in range({n})];a.sort();print(len(a),sum(a),a[0],a[-1])'
for n in (10000,40000):programs['bigint-'+str(n)]=f'x=(1<<{n})-1;y=x*x;print(y.bit_length(),y%1000000007)'
programs['stdlib']='import _bisect,_heapq,_json,math,zlib,hashlib,_ssl,_hashlib,_bz2,_lzma,sqlite3;print(math.factorial(30),zlib.crc32(b"abc"*1000),hashlib.sha256(b"abc").hexdigest())'
results=[]
for name,program in programs.items():
 plain=invoke('plain',program);meter=invoke('meter',program);assert plain['output']==meter['output'];results.append({'name':name,'plain':plain,'meter':meter})
loop=[r['meter']['work_units'] for r in results if r['name'].startswith('loop-')]
(ROOT/'provenance/smoke-intermediate.json').write_text(json.dumps({'results':results,'loop_work':loop},indent=2)+'\n')
print(json.dumps({'python_loop_work':loop}))
assert loop[0]<loop[1]<loop[2]
for prefix in ('sort-','bigint-'):
 values=[r['meter']['work_units'] for r in results if r['name'].startswith(prefix)]
 assert values[0]<values[1],(prefix,values)
# Fixed native loop is the exact scaling test; Python paths include allocation,
# specialization, varying integer operands, startup and hash-dependent work.
native=[]
for n in (100000,200000,400000):
 with tempfile.TemporaryDirectory() as d:
  path=pathlib.Path(d)/'native.json';run=subprocess.run([str(ROOT/'provenance/counter-smoke'),str(n)],env=dict(os.environ,WM_REPORT_PATH=str(path),WM_EXPECT_METER='1'),capture_output=True,timeout=30)
  assert run.returncode==0
  m=json.loads(path.read_text());assert m['valid'] and m['work_units']>0
  expected=sum(metadata[module['metadata_id']]['blocks'][i][1]*count for module in m['modules'] for i,count in module['counts'])
  assert expected==m['work_units'];native.append(expected)
assert native[2]-native[1]==2*(native[1]-native[0]),native
pure=[]
for n in (131072,262144,524288):
 with tempfile.TemporaryDirectory() as d:
  path=pathlib.Path(d)/'pure.json';argv=[str(n),'pure']
  plain=subprocess.run([str(ROOT/'provenance/counter-smoke-plain'),*argv],capture_output=True,timeout=30)
  run=subprocess.run([str(ROOT/'provenance/counter-smoke'),*argv],env=dict(os.environ,WM_REPORT_PATH=str(path),WM_EXPECT_METER='1'),capture_output=True,timeout=30)
  assert plain.returncode==run.returncode==0 and plain.stdout==run.stdout
  m=json.loads(path.read_text());assert m['valid']
  work=sum(metadata[module['metadata_id']]['blocks'][i][1]*count for module in m['modules'] for i,count in module['counts'] if 'pure_work' in metadata[module['metadata_id']]['blocks'][i][2])
  assert work>0;pure.append(work)
assert pure[2]-pure[1]==2*(pure[1]-pure[0]),pure
# Thread creation is rejected before instrumented code can race. Both controls
# and metered executables must expose the same unsupported-runtime boundary.
for variant in ('plain','meter'):
 with tempfile.TemporaryDirectory() as d:
  path=pathlib.Path(d)/'threads.json'
  r=subprocess.run([str(ROOT/variant/'bin/python3.13'),'-I','-S','-c','import threading;t=threading.Thread(target=lambda:None);t.start();t.join()'],env=dict(os.environ,WM_REPORT_PATH=str(path),WM_EXPECT_METER=str(int(variant=='meter'))),capture_output=True,timeout=30)
  assert r.returncode!=0
  m=json.loads(path.read_text());assert m['valid'] is False and m['flags']&8 and m['work_units'] is None
 # libc fork is outside the counting scope. The child cannot steal the report.
 with tempfile.TemporaryDirectory() as d:
  path=pathlib.Path(d)/'fork.json'
  program='import os,sys\npid=os.fork()\nif pid==0: sys.exit(0)\nos.waitpid(pid,0)'
  r=subprocess.run([str(ROOT/variant/'bin/python3.13'),'-I','-S','-c',program],env=dict(os.environ,WM_REPORT_PATH=str(path),WM_EXPECT_METER=str(int(variant=='meter'))),capture_output=True,timeout=30)
  assert r.returncode==0
  m=json.loads(path.read_text());assert m['valid'] is False and m['flags']&64 and m['work_units'] is None
manifest={'schema':1,'files':{p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted((ROOT/'metadata').glob('*.json'))}}
(ROOT/'metadata-manifest.json').write_text(json.dumps(manifest,sort_keys=True)+'\n')
(ROOT/'provenance/smoke.json').write_text(json.dumps({'exit_code':0,'results':results,'native_loop_work':native,'pure_function_work':pure,'python_increment_ratio':(loop[2]-loop[1])/(loop[1]-loop[0]),'unsupported_thread_detected':True,'thread_creation_rejected':True,'unsupported_fork_detected':True},indent=2)+'\n')
print(json.dumps({'smoke':'passed','programs':len(results),'metadata_files':len(metadata)}))
