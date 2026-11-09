#define _GNU_SOURCE
#include <errno.h>
#include <string.h>
#include <grp.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <unistd.h>

static void fail(const char *what) { perror(what); _exit(126); }
int main(int argc, char **argv) {
    if (argc < 2) return 126;
    int first = 1;
    unsigned long seconds = 20;
    if (argc > 2 && strcmp(argv[1], "--cpu-seconds") == 0) {
        char *end = NULL;
        errno = 0;
        seconds = strtoul(argv[2], &end, 10);
        if (errno || !*argv[2] || *end || seconds < 1 || seconds > 60) return 126;
        first = 3;
    }
    if (argc <= first) return 126;
    struct rlimit cpu = {seconds, seconds + 1}, files = {67108864, 67108864}, fds = {256, 256};
    if (setrlimit(RLIMIT_CPU, &cpu) || setrlimit(RLIMIT_FSIZE, &files) ||
        setrlimit(RLIMIT_NOFILE, &fds)) fail("setrlimit");
    if (geteuid() == 0 && (setgroups(0, NULL) || setgid(65532) || setuid(65532)))
        fail("drop uid/gid");
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)) fail("no_new_privs");
    const int denied[] = {SYS_socket, SYS_socketpair, SYS_connect, SYS_bind,
        SYS_listen, SYS_accept, SYS_accept4, SYS_sendto, SYS_sendmsg, SYS_sendmmsg,
        SYS_recvfrom, SYS_recvmsg, SYS_recvmmsg, SYS_ptrace, SYS_process_vm_readv,
        SYS_process_vm_writev, SYS_unshare, SYS_setns, SYS_mount, SYS_umount2,
        SYS_pivot_root, SYS_chroot, SYS_reboot, SYS_init_module, SYS_finit_module,
        SYS_delete_module, SYS_bpf, SYS_perf_event_open, SYS_keyctl,
        SYS_kexec_load, SYS_kexec_file_load, SYS_swapon, SYS_swapoff};
    struct sock_filter filter[128]; unsigned n = 0;
    filter[n++] = (struct sock_filter)BPF_STMT(BPF_LD|BPF_W|BPF_ABS, offsetof(struct seccomp_data, arch));
    filter[n++] = (struct sock_filter)BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K, AUDIT_ARCH_X86_64, 1, 0);
    filter[n++] = (struct sock_filter)BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_KILL_PROCESS);
    filter[n++] = (struct sock_filter)BPF_STMT(BPF_LD|BPF_W|BPF_ABS, offsetof(struct seccomp_data, nr));
    filter[n++] = (struct sock_filter)BPF_JUMP(BPF_JMP|BPF_JSET|BPF_K, 0x40000000, 0, 1);
    filter[n++] = (struct sock_filter)BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_ERRNO|EPERM);
    for (unsigned i = 0; i < sizeof(denied)/sizeof(denied[0]); ++i) {
        filter[n++] = (struct sock_filter)BPF_JUMP(BPF_JMP|BPF_JEQ|BPF_K, (unsigned)denied[i], 0, 1);
        filter[n++] = (struct sock_filter)BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_ERRNO|EPERM);
    }
    filter[n++] = (struct sock_filter)BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_ALLOW);
    struct sock_fprog program = {.len = (unsigned short)n, .filter = filter};
    if (prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &program)) fail("seccomp");
    execvp(argv[first], argv+first); fail("execvp");
}
