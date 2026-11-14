#!/usr/bin/env python3
"""Exercise the pinned CPU-policy runtime without requiring root or the sandbox.

Usage: TMPDIR=/path/with/space python3 scripts/test_cpu_policy_runtime.py RUNTIME_DIR
Requires Linux x86-64, Python 3 and a C compiler with static libc development files.
RUNTIME_DIR contains qemu-x86_64 and cpu-policy.so. Builds only authored fixtures
inside a private temporary directory; LD_PRELOAD below is a test observer, not
part of the deployed runtime. Inherit LD_LIBRARY_PATH for locally built QEMU.
"""
import argparse
import json
import os
import pathlib
import resource
import subprocess
import sys
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("runtime", type=pathlib.Path)
args = parser.parse_args()
runtime = args.runtime.resolve()
qemu, plugin = str(runtime / "qemu-x86_64"), str(runtime / "cpu-policy.so")
workspace = tempfile.TemporaryDirectory(prefix="aonohako-cpu-policy-test-")
root = pathlib.Path(workspace.name)
env = dict(os.environ, TMPDIR=str(root))
compiler = os.environ.get("CC", "cc")
preload = root / 'marker.so'
(root / 'marker.c').write_text(r'''
#define _GNU_SOURCE
#include <stdint.h>
#include <stdlib.h>
#include <sys/mman.h>
#include <sys/syscall.h>
#include <unistd.h>
__attribute__((constructor)) static void init(void) {
    uintptr_t pages[] = { UINT64_C(0x20000000000), UINT64_C(0x8000000000) };
    for (unsigned i = 0; i < 2; i++) {
        void *p = mmap((void *)pages[i], 4096, PROT_READ | PROT_WRITE,
                      MAP_SHARED | MAP_FIXED_NOREPLACE, atoi(getenv("AONOHAKO_MARKER_FD")), i * 4096);
        if (p == MAP_FAILED || (uintptr_t)p != pages[i]) _exit(111);
        *(uint64_t *)p = UINT64_C(0x4242424242424242);
    }
}
/* Capture actual guest memory when the policy terminates the emulator. This
 * proves forbidden stores are stopped before their memory side effects. */
__attribute__((noreturn)) void _exit(int status) {
    const char *fd = getenv("AONOHAKO_OBSERVE_FD");
    if (fd) syscall(SYS_write, atoi(fd), (void *)UINT64_C(0x10020000000), 16);
    syscall(SYS_exit_group, status);
    __builtin_unreachable();
}
''')
subprocess.run([compiler, '-shared', '-fPIC', '-O2', '-o', str(preload), str(root / 'marker.c')], check=True, env=env)
start = '.global _start\n.text\n_start:\n'
finish = '''
mov $1, %eax
mov $1, %edi
lea reached(%rip), %rsi
mov $8, %edx
syscall
mov $60, %eax
xor %edi, %edi
syscall
.section .rodata
reached: .ascii "REACHED\\n"
'''
mm_last_page = '''
mov $9,%eax
mov $0xfffff000,%edi
mov $4096,%esi
mov $3,%edx
mov $0x32,%r10d
mov $-1,%r8
xor %r9d,%r9d
syscall
cmp $0xfffff000,%eax
jne setup_failed
'''
setup_failed = '''
.text
setup_failed:
mov $60,%eax
mov $113,%edi
syscall
'''
fixtures = {}
def add(name, body, expected='fault', feature='avx', suffix='', cpu='qemu64', observe=False):
    fixtures[name] = (start + body + finish + suffix, expected, feature, cpu, observe)
add('scalar_ok', 'xor %eax,%eax\n', 'pass')
add('unreachable_avx', 'jmp skip\n.byte 0xc5,0xf8,0x77\nskip:\n', 'pass')
add('scalar_sse_ok', 'pxor %xmm0,%xmm0\naddsd %xmm0,%xmm0\n', 'pass', 'simd')
add('raw_avx', '.byte 0xc5,0xf8,0x77\n', 'RV')
add('packed_simd', 'addps %xmm0,%xmm0\n', 'RV', 'simd')
add('unused_after_fault', 'xor %eax,%eax\nmov (%rax),%rax\n.byte 0xc5,0xf8,0x77\n')
for name, op in {
    'scalar_load': 'mov (%rax),%rbx',
    'scalar_store': 'movq $123,(%rax)',
    'vector_load': 'movups (%rax),%xmm0',
    'vector_store': 'movups %xmm0,(%rax)',
    'atomic': 'lock addq $1,(%rax)',
    'x87_load': 'fldt (%rax)',
    'x87_store': 'fld1\nfstpt (%rax)',
    'fxsave': 'fxsave (%rax)',
    'code_fetch': 'jmp *%rax',
}.items():
    add('alias_above_' + name, 'movabs $0x10000000000,%rax\n' + op + '\n')
add('alias_below_load', 'movabs $0xffffff8000000000,%rax\nmov (%rax),%rbx\n')
add('prefetch_alias', 'movabs $0x10000000000,%rax\nprefetcht0 (%rax)\n', 'pass')
add('alias_below_store', 'movabs $0xffffff8000000000,%rax\nmovq $123,(%rax)\n')
# Futex hands translated pointers to the host kernel; both pointer arguments
# must stay inside the guest window, including FUTEX_WAKE_OP's second address.
add('futex_alias', 'mov $202,%eax\nmovabs $0x10000000000,%rdi\nmov $1,%esi\nmov $1,%edx\nsyscall\n')
add('futex_second_alias', 'mov $202,%eax\nlea futex_word(%rip),%rdi\nmov $5,%esi\nmov $1,%edx\nxor %r10d,%r10d\nmovabs $0x10000000000,%r8\nxor %r9d,%r9d\nsyscall\n', suffix='.data\n.p2align 2\nfutex_word: .long 0\n')
for segment, code in [('fs',0x1002),('gs',0x1001)]:
    add('alias_' + segment, f'mov $158,%eax\nmov ${code},%edi\nmov $4096,%esi\nsyscall\nmovabs $0xfffffff000,%rax\nmov %{segment}:(%rax),%rbx\n')
for name, addr, op in [('last_byte',0xffffffff,'movb $123,(%rax)'),('last_qword',0xfffffff8,'movq $123,(%rax)')]:
    add('boundary_' + name, mm_last_page + f'movabs ${addr},%rax\n' + op + '\n', 'pass', suffix=setup_failed)
for name, addr, op in [('scalar',0xfffffffc,'mov (%rax),%rbx'),('vector',0xfffffff8,'movups (%rax),%xmm0'),('store',0xffffffff,'movq $123,(%rax)'),('x87',0xfffffff8,'fldt (%rax)')]:
    add('cross_boundary_' + name, mm_last_page + f'movabs ${addr},%rax\n' + op + '\n', suffix=setup_failed)
for name, num in [('open',2),('execve',59),('clone',56),('clone3',435),('dup',32),('sendfile',40)]:
    add('syscall_' + name, f'mov ${num},%eax\nxor %edi,%edi\nsyscall\n', 'RS')
add('fd_write', 'mov $1,%eax\nmov $REPORT_FD,%edi\nlea reached(%rip),%rsi\nmov $1,%edx\nsyscall\n', 'RS')
add('fd_close', 'mov $3,%eax\nmov $REPORT_FD,%edi\nsyscall\n', 'RS')
add('fd_mmap', 'mov $9,%eax\nxor %edi,%edi\nmov $4096,%esi\nmov $3,%edx\nmov $1,%r10d\nmov $REPORT_FD,%r8d\nxor %r9d,%r9d\nsyscall\n', 'RS')
add('guard_mprotect', 'mov $10,%eax\nmovabs $0x100000000,%rdi\nmov $4096,%esi\nmov $3,%edx\nsyscall\n', 'RS')
add('jit_avx', '''
mov $9,%eax
xor %edi,%edi
mov $4096,%esi
mov $7,%edx
mov $0x22,%r10d
mov $-1,%r8
xor %r9d,%r9d
syscall
movl $0xc377f8c5,(%rax)
call *%rax
''', 'RV')
add('selfmodify_avx', '''
call victim
lea victim(%rip),%r12
mov %r12,%rdi
and $-4096,%rdi
mov $4096,%esi
mov $7,%edx
mov $10,%eax
syscall
movl $0xc377f8c5,(%r12)
call *%r12
''', 'RV', suffix='.text\nvictim: ret\nnop\nnop\nnop\n')
add('interior_avx', 'lea immediate+2(%rip),%rax\njmp *%rax\n', 'RV', suffix='.text\nimmediate: .byte 0x48,0xb8,0xc5,0xf8,0x77,0xc3,0,0,0,0\n')
add('signal_handler_avx', '''
mov $13,%eax
mov $4,%edi
lea action(%rip),%rsi
xor %edx,%edx
mov $8,%r10d
syscall
ud2
''', 'RV', suffix='''.text
handler: .byte 0xc5,0xf8,0x77
mov $60,%eax
mov $114,%edi
syscall
restorer: mov $15,%eax
syscall
.data
.p2align 3
action: .quad handler,0x04000000,restorer,0
''')
add('exit126', 'mov $60,%eax\nmov $126,%edi\nsyscall\n', 'exit126')
# The controls run the same stores without the plugin and must modify the
# observed buffer, proving the observation itself is effective.
for name, instruction, feature, cpu in [
    ('avx_store', 'vmovups %xmm0,(%rdi)', 'avx', 'max'),
    ('simd_store', 'maskmovdqu %xmm1,%xmm0', 'simd', 'qemu64'),
]:
    body = 'movdqa ones(%rip),%xmm0\nmovdqa ones(%rip),%xmm1\nlea effect(%rip),%rdi\n' + instruction + '\n'
    suffix = '.section .effect,"aw",@nobits\neffect: .zero 16\n.section .rodata\n.p2align 4\nones: .fill 16,1,255\n'
    add(name + '_blocked', body, 'RV', feature, suffix, cpu, True)
    add(name + '_control', body, 'control', feature, suffix, cpu, True)
# A forged return requests the existing 32-bit data segment as CS. This pinned
# x86-64-only translator rejects that mode before the compat payload executes.
add('forged_cs_syscall', '''
mov $13,%eax
mov $4,%edi
lea action(%rip),%rsi
xor %edx,%edx
mov $8,%r10d
syscall
ud2
''', 'abort', suffix='''.text
handler: movw $0x2b,184(%rdx)
lea compat(%rip),%rax
mov %rax,168(%rdx)
ret
restorer: mov $15,%eax
syscall
.code32
compat: mov $59,%eax
xor %edi,%edi
xor %esi,%esi
xor %edx,%edx
syscall
.code64
.data
.p2align 3
action: .quad handler,0x04000004,restorer,0
''')
# Static libc startup/stdio must work with the launcher's baseline CPU model.
fixtures['glibc_stdio'] = ('#include <stdio.h>\nint main(void) { puts("REACHED"); return 0; }\n', 'pass', 'avx', 'qemu64', False)
results = []
for name, (asm, expected, feature, cpu, observe) in fixtures.items():
    report = root / (name + '.report')
    fd = os.open(report, os.O_RDWR | os.O_CREAT | os.O_TRUNC, 0o600)
    asm = asm.replace('REPORT_FD', str(fd))
    is_c = name == 'glibc_stdio'
    src, binary = root / (name + ('.c' if is_c else '.S')), root / name
    src.write_text(asm if is_c else asm + '\n.section .note.GNU-stack,"",@progbits\n')
    flags = [] if is_c else ['-nostdlib']
    if observe:
        flags += ['-Wl,--section-start=.effect=0x20000000']
    subprocess.run([compiler, *flags, '-static', '-no-pie', '-Wl,-z,noexecstack', '-o', str(binary), str(src)], env=env, check=True)
    marker = root / (name + '.marker')
    marker_fd = os.open(marker, os.O_RDWR | os.O_CREAT | os.O_TRUNC, 0o600)
    os.ftruncate(marker_fd, 8192)
    observation = root / (name + '.observation')
    observation_fd = os.open(observation, os.O_RDWR | os.O_CREAT | os.O_TRUNC, 0o600)
    runenv = dict(env, LD_PRELOAD=str(preload), AONOHAKO_MARKER_FD=str(marker_fd))
    if observe:
        runenv['AONOHAKO_OBSERVE_FD'] = str(observation_fd)
    else:
        runenv.pop('AONOHAKO_OBSERVE_FD', None)
    command = [qemu, '-B', '0x10000000000', '-R', '4G', '-cpu', cpu]
    if expected != 'control':
        command += ['-plugin', plugin + ',' + feature + '=on,report-fd=' + str(fd)]
    command.append(str(binary))
    def limits():
        resource.setrlimit(resource.RLIMIT_CORE, (0,0))
    result = subprocess.run(command, env=runenv, pass_fds=(fd,marker_fd,observation_fd), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10, preexec_fn=limits)
    for opened_fd in (fd, marker_fd, observation_fd):
        os.close(opened_fd)
    status = report.read_bytes()
    if expected == 'pass':
        okay = result.returncode == 0 and status == b'R' and result.stdout == b'REACHED\n'
    elif expected == 'control':
        okay = result.returncode == 0 and status == b'' and result.stdout == b'REACHED\n'
    elif expected in ('RV','RS'):
        okay = result.returncode == 126 and status == expected.encode() and result.stdout == b''
    elif expected == 'exit126':
        okay = result.returncode == 126 and status == b'R' and result.stdout == b''
    elif expected == 'abort':
        # GLib writes its assertion diagnostic to stdout as well as stderr.
        okay = result.returncode == -6 and status == b'R' and b'REACHED' not in result.stdout
    else:
        okay = result.returncode in (-11, 127) and status == b'R' and result.stdout == b''
    marker_bytes = marker.read_bytes()
    okay &= marker_bytes[:8] == b'B' * 8 and marker_bytes[4096:4104] == b'B' * 8
    if observe:
        okay &= observation.read_bytes() == (b'\xff' if expected == 'control' else b'\x00') * 16
    record = dict(name=name, okay=okay, exit=result.returncode, report=status.decode(errors='replace'), stdout=result.stdout[:128].decode(errors='replace'), stderr=result.stderr[:256].decode(errors='replace'))
    results.append(record)
    print(('PASS ' + name) if okay else json.dumps(record), flush=True)
print(f'{sum(r["okay"] for r in results)}/{len(results)} passed')
workspace.cleanup()
sys.exit(0 if all(r['okay'] for r in results) else 1)
