#!/bin/bash
set -euo pipefail
install -d /opt/interpreter-meter/cpython/{lib,metadata,provenance}
python3 /src/test_runtime.py
WM_LLVM_VERSION=19 python3 /src/standalone.py
cp /src/standalone-local/{results.json,cross_tu.ll} /opt/interpreter-meter/cpython/provenance/
clang-19 -O2 -fPIC -shared /src/runtime.c -o /opt/interpreter-meter/cpython/lib/libworkmeter.so -ldl -pthread
cp /src/standalone-local/WorkMeter.so /tmp/WorkMeter.so
WM_METADATA_DIR=/opt/interpreter-meter/cpython/metadata clang-19 -O3 -fpass-plugin=/tmp/WorkMeter.so /src/counter_smoke.c -L/opt/interpreter-meter/cpython/lib -Wl,-rpath,/opt/interpreter-meter/cpython/lib -lworkmeter -o /opt/interpreter-meter/cpython/provenance/counter-smoke
clang-19 -O3 /src/counter_smoke.c -L/opt/interpreter-meter/cpython/lib -Wl,-rpath,/opt/interpreter-meter/cpython/lib -lworkmeter -o /opt/interpreter-meter/cpython/provenance/counter-smoke-plain
curl --fail --location --silent --show-error https://www.python.org/ftp/python/3.13.5/Python-3.13.5.tar.xz -o /tmp/Python-3.13.5.tar.xz
echo '93e583f243454e6e9e4588ca2c2662206ad961659863277afcdb96801647d640  /tmp/Python-3.13.5.tar.xz' | sha256sum --check
sha256sum /tmp/Python-3.13.5.tar.xz > /opt/interpreter-meter/cpython/provenance/source.sha256
clang-19 --version > /opt/interpreter-meter/cpython/provenance/compiler.txt
llvm-config-19 --version > /opt/interpreter-meter/cpython/provenance/llvm.txt
for variant in plain meter; do
 mkdir /tmp/python-$variant
 tar -xJf /tmp/Python-3.13.5.tar.xz -C /tmp/python-$variant --strip-components=1
 cd /tmp/python-$variant
 cp /src/hashseed_main.c Programs/python.c
 export WM_METADATA_DIR=/opt/interpreter-meter/cpython/metadata
 extra=''
 if [ "$variant" = meter ]; then extra='-fpass-plugin=/tmp/WorkMeter.so'; fi
 # Configure probes must see the host's real thread capabilities. The
 # diagnostic runtime rejects pthread_create and is linked only for the build.
 CC=clang-19 CXX=clang++-19 CFLAGS='-O3 -fno-omit-frame-pointer' ./configure --prefix=/opt/interpreter-meter/cpython/$variant --with-ensurepip=no --disable-test-modules
 build_cflags="-O3 -fno-omit-frame-pointer $extra"
 build_ldflags='-Wl,--no-as-needed -L/opt/interpreter-meter/cpython/lib -Wl,-rpath,/opt/interpreter-meter/cpython/lib -lworkmeter -Wl,--as-needed'
 python3 -c 'import json,sys;json.dump({"CFLAGS_NODIST":sys.argv[1],"LDFLAGS_NODIST":sys.argv[2],"compileall_jobs":1},open(sys.argv[3],"w"),indent=2)' "$build_cflags" "$build_ldflags" /opt/interpreter-meter/cpython/provenance/$variant-build-arguments.json
 make -j4 CFLAGS_NODIST="$build_cflags" LDFLAGS_NODIST="$build_ldflags"
 make install -j4 COMPILEALL_OPTS=-j1 CFLAGS_NODIST="$build_cflags" LDFLAGS_NODIST="$build_ldflags"
 cp config.log /opt/interpreter-meter/cpython/provenance/$variant-config.log
 /opt/interpreter-meter/cpython/$variant/bin/python3.13 -I -S -c 'import sys,sysconfig,json;print(json.dumps({"version":sys.version,"config":sysconfig.get_config_vars()},default=str))' > /opt/interpreter-meter/cpython/provenance/$variant-config.json
 done
cp /src/{runtime.c,WorkMeter.cpp,build.sh,Dockerfile,smoke.py,counter_smoke.c,standalone.py,hashseed_main.c,hashseed_smoke.py,test_runtime.py} /opt/interpreter-meter/cpython/provenance/
python3 /src/hashseed_smoke.py
python3 /src/smoke.py
