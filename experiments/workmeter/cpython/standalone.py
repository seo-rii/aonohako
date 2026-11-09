"""Bounded native correctness gate; set WM_LLVM_VERSION for LLVM 16 or 19."""
from pathlib import Path
import os,json,subprocess,tempfile,shlex,hashlib,uuid
ROOT=Path(__file__).resolve().parent;OUT=Path(os.environ.get('WM_TEST_OUTPUT',ROOT/'standalone-local')).resolve();OUT.mkdir(parents=True,exist_ok=True);META=OUT/'metadata';META.mkdir(exist_ok=True)
version=os.environ.get('WM_LLVM_VERSION','16')
clang='clang-'+version;cxx='clang++-'+version;config='llvm-config-'+version
def run(args,**kw):
 r=subprocess.run(args,capture_output=True,text=True,timeout=120,**kw)
 if r.returncode:raise RuntimeError('command failed: '+str(args[:2])+'\n'+r.stderr[-6000:])
 return r
flags=shlex.split(run([config,'--cxxflags','--ldflags','--system-libs','--libs','core','passes']).stdout)
run([cxx,'-shared','-fPIC',str(ROOT/'WorkMeter.cpp'),'-o',str(OUT/'WorkMeter.so'),*flags,'-O2'])
run([clang,'-O2','-shared','-fPIC',str(ROOT/'runtime.c'),'-o',str(OUT/'libworkmeter.so'),'-ldl','-pthread'])
(OUT/'pure_external.c').write_text('''#include <stdint.h>
__attribute__((const,noinline)) uint64_t external_work(uint64_t n){uint64_t sum=0;for(uint64_t i=0;i<n;i++)sum+=((i*UINT64_C(0x9e3779b97f4a7c15))^(i>>7))%UINT64_C(1000000007);return sum;}
''')
(OUT/'cross_tu.c').write_text('''#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
__attribute__((const)) uint64_t external_work(uint64_t);
int main(int argc,char**argv){if(argc!=2)return 2;printf("%llu\\n",(unsigned long long)external_work(strtoull(argv[1],0,10)));return 0;}
''')
(OUT/'computed.c').write_text('''#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
int main(int argc,char**argv){if(argc!=2)return 2;uint64_t n=strtoull(argv[1],0,10),i=0,x=0;static void*labels[]={&&a,&&b,&&c};
next:if(i>=n)goto done;goto *labels[i%3];
a:x+=i*3+1;i++;goto next;
b:x^=i*7+3;i++;goto next;
c:x+=i*11+5;i++;goto next;
done:printf("%llu\\n",(unsigned long long)x);return 0;}
''')
units={'loop':[ROOT/'counter_smoke.c'],'cross':[OUT/'cross_tu.c',OUT/'pure_external.c'],'computed':[OUT/'computed.c']}
for name,sources in units.items():
 for variant in ('plain','meter'):
  cmd=[clang,'-O3',*map(str,sources),'-L'+str(OUT),'-Wl,-rpath,'+str(OUT),'-Wl,--no-as-needed','-lworkmeter','-o',str(OUT/(name+'-'+variant))]
  if variant=='meter':cmd.insert(2,'-fpass-plugin='+str(OUT/'WorkMeter.so'))
  run(cmd,env=dict(os.environ,WM_METADATA_DIR=str(META)))
# Preserve emitted optimized/instrumented IR for definition, declaration and call-site review.
run([clang,'-O3','-fpass-plugin='+str(OUT/'WorkMeter.so'),'-S','-emit-llvm',str(OUT/'cross_tu.c'),'-o',str(OUT/'cross_tu.ll')],env=dict(os.environ,WM_METADATA_DIR=str(META)))
run(['opt-'+version,'--passes=verify','--disable-output',str(OUT/'cross_tu.ll')])
metadata={p.stem:json.loads(p.read_text()) for p in META.glob('*.json')}
results=[]
for name,extra,levels,function in [('loop',[],[100000,200000,400000],None),('loop',['pure'],[131072,262144,524288],'pure_work'),('cross',[],[131072,262144,524288],'external_work'),('computed',[],[393216,786432,1572864],None)]:
 values=[]
 for n in levels:
  args=[str(n),*extra];plain=run([str(OUT/(name+'-plain')),*args]);report=OUT/(name+'-'+str(n)+('-pure' if extra else '')+'-'+uuid.uuid4().hex+'.json')
  metered=run([str(OUT/(name+'-meter')),*args],env=dict(os.environ,WM_REPORT_PATH=str(report),WM_EXPECT_METER='1'))
  assert plain.stdout==metered.stdout
  m=json.loads(report.read_text());assert m['valid']
  total=0;selected=0
  for module in m['modules']:
   md=metadata[module['metadata_id']];assert module['blocks']==len(md['blocks'])
   for index,count in module['counts']:
    w=md['blocks'][index][1]*count;total+=w
    if function is None or function in md['blocks'][index][2]:selected+=w
  assert total==m['work_units'] and selected>0
  values.append(selected)
 assert values[2]-values[1]==2*(values[1]-values[0]),(name,extra,values)
 results.append({'program':name,'mode':extra,'n':levels,'selected_work':values,'increment_ratio':(values[2]-values[1])/(values[1]-values[0])})
summary={'exit_code':0,'llvm_version':run([config,'--version']).stdout.strip(),'tests':results,'source_sha256':{n:hashlib.sha256((ROOT/n).read_bytes()).hexdigest() for n in ('WorkMeter.cpp','runtime.c','counter_smoke.c')}}
(OUT/'results.json').write_text(json.dumps(summary,indent=2)+'\n');print(json.dumps(summary))
