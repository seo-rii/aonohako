"""Prepare a public-only diagnostic image context; no deployment or credentials."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
from suite import generate

ROOT=Path(__file__).resolve().parent

def prepare(output, runtime_manifest):
    if output.exists(): raise ValueError('output must not exist')
    runtime=json.loads(runtime_manifest.read_bytes())
    if not isinstance(runtime.get('image'),str) or not runtime['image']:
        raise ValueError('runtime manifest requires an image identity')
    fingerprint=runtime.get('metadata_manifest_sha256','')
    if len(fingerprint)!=64 or any(c not in '0123456789abcdef' for c in fingerprint):
        raise ValueError('runtime manifest requires metadata SHA-256')
    output.mkdir(mode=0o700,parents=True)
    for name in ('Dockerfile','server.py','runner.py','protocol.py','validate.py','preflight.py'):
        shutil.copyfile(ROOT/name,output/name)
    (output/'pypy').mkdir(mode=0o700)
    for name in ('runner.py','meter.py','adapter.py'):
        target=output/'pypy'/name
        shutil.copyfile(ROOT/'pypy'/name,target)
        target.chmod(0o444)
    (output/'pypy').chmod(0o555)
    generate(output/'fixtures')
    runtime['fixture_manifest_sha256']=hashlib.sha256((output/'fixtures/manifest.json').read_bytes()).hexdigest()
    (output/'runtime-manifest.json').write_text(json.dumps(runtime,indent=2)+'\n')
    subprocess.run(['cc','-static','-O2','-Wall','-Wextra','-Werror',str(ROOT/'launch.c'),'-o',str(output/'launch')],check=True)
    hashes={str(p.relative_to(output)):hashlib.sha256(p.read_bytes()).hexdigest()
            for p in output.rglob('*') if p.is_file()}
    (output/'source-manifest.json').write_text(json.dumps({'schema':1,'files':hashes},indent=2)+'\n')
    return hashes

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output',type=Path,required=True)
    p.add_argument('--runtime-manifest',type=Path,required=True)
    args=p.parse_args();os.umask(0o077)
    print(json.dumps({'files':len(prepare(args.output,args.runtime_manifest)),'output':str(args.output)}))
