/* SPDX-License-Identifier: GPL-2.0-or-later */
/*
 * QEMU TCG instruction policy for static x86-64 standard-I/O submissions.
 * The outer aonohako sandbox remains responsible for host isolation and limits.
 * The launcher must reserve a separate guest address range with -B and -R and
 * reject dynamic executables. Guest processes, file contents, and non-stdio descriptors
 * are unavailable: in particular, execve must never escape to native execution.
 *
 * Keep instruction classification aligned with internal/cpupolicy/policy.go.
 * QEMU invokes instruction callbacks before executing the guest instruction.
 * Translation alone never terminates a submission, including undecodable code.
 */
#define _GNU_SOURCE
#include "qemu-plugin-compat.h"

#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

QEMU_PLUGIN_EXPORT int qemu_plugin_version = AONOHAKO_QEMU_PLUGIN_API_VERSION;

enum decision { ALLOW, DENY_AVX, DENY_SIMD, DENY_DECODE, DENY_CONTROL };
static bool deny_avx;
static bool deny_simd;
/* The launcher owns this regular file; guest descriptor access is denied. Its
 * protocol is R (first guest execution), RV (instruction violation), or RS
 * (syscall violation). Guest exit status/stderr cannot forge this verdict. */
static int report_fd = -1;
static bool report_ready;

#define GUEST_ADDRESS_LIMIT (UINT64_C(1) << 32)
#define ARRAY_LENGTH(a) (sizeof(a) / sizeof((a)[0]))

static bool member(const char *value, const char *const *values, size_t count)
{
    for (size_t i = 0; i < count; ++i) {
        if (strcmp(value, values[i]) == 0) {
            return true;
        }
    }
    return false;
}

static bool word_char(unsigned char c)
{
    return isalnum(c) || c == '_';
}

static char *trim(char *value)
{
    while (isspace((unsigned char)*value)) {
        ++value;
    }
    char *end = value + strlen(value);
    while (end > value && isspace((unsigned char)end[-1])) {
        *--end = '\0';
    }
    return value;
}

static bool prefix(const char *word)
{
    static const char *const prefixes[] = {
        "lock", "rep", "repz", "repe", "repnz", "repne", "bnd", "notrack",
        "data16", "addr16", "addr32", "cs", "ds", "es", "fs", "gs", "ss",
    };
    return member(word, prefixes, ARRAY_LENGTH(prefixes)) ||
        strcmp(word, "rex") == 0 || strncmp(word, "rex.", 4) == 0;
}

static bool xmm_operand(const char *value)
{
    if (*value == '%') {
        ++value;
    }
    if (strncmp(value, "xmm", 3) != 0 || !isdigit((unsigned char)value[3])) {
        return false;
    }
    value += 3;
    while (isdigit((unsigned char)*value)) {
        ++value;
    }
    return *value == '\0';
}

static bool scalar_xmm(const char *mnemonic, char *operands)
{
    static const char *const scalar[] = {
        "movss", "movsd", "movd", "movq", "movaps", "movapd", "movups", "movupd", "movdqa", "movdqu",
        "addss", "addsd", "subss", "subsd", "mulss", "mulsd", "divss", "divsd",
        "minss", "minsd", "maxss", "maxsd", "sqrtss", "sqrtsd", "rsqrtss", "rcpss", "roundss", "roundsd",
        "comiss", "comisd", "ucomiss", "ucomisd", "cvtss2sd", "cvtsd2ss",
        "cvtsi2ss", "cvtsi2ssl", "cvtsi2ssq", "cvtsi2sd", "cvtsi2sdl", "cvtsi2sdq",
        "cvtss2si", "cvtss2sil", "cvtss2siq", "cvtsd2si", "cvtsd2sil", "cvtsd2siq",
        "cvttss2si", "cvttss2sil", "cvttss2siq", "cvttsd2si", "cvttsd2sil", "cvttsd2siq",
    };
    if (member(mnemonic, scalar, ARRAY_LENGTH(scalar))) {
        return true;
    }
    if (strcmp(mnemonic, "pxor") == 0 || strcmp(mnemonic, "xorps") == 0 || strcmp(mnemonic, "xorpd") == 0) {
        char *comma = strchr(operands, ',');
        if (!comma || strchr(comma + 1, ',')) {
            return false;
        }
        *comma = '\0';
        char *left = trim(operands), *right = trim(comma + 1);
        return strcmp(left, right) == 0 && xmm_operand(left);
    }
    static const char *const predicates[] = { "", "eq", "lt", "le", "unord", "neq", "nlt", "nle", "ord" };
    size_t length = strlen(mnemonic);
    if (length >= 5 && strncmp(mnemonic, "cmp", 3) == 0 &&
        (strcmp(mnemonic + length - 2, "ss") == 0 || strcmp(mnemonic + length - 2, "sd") == 0)) {
        char condition[16];
        size_t count = length - 5;
        if (count < sizeof(condition)) {
            memcpy(condition, mnemonic + 3, count);
            condition[count] = '\0';
            return member(condition, predicates, ARRAY_LENGTH(predicates));
        }
    }
    return false;
}

/* Accept both GNU/Capstone AT&T and the documented Capstone Intel fallback. */
static enum decision classify(const char *disassembly)
{
    char buffer[512];
    if (!disassembly || strnlen(disassembly, sizeof(buffer)) == sizeof(buffer)) {
        return DENY_DECODE;
    }
    strcpy(buffer, disassembly);
    char *body = trim(buffer);
    char *annotation = strpbrk(body, "#<");
    if (annotation) {
        *annotation = '\0';
    }
    if (strchr(body, '\n') || strchr(body, '\r')) {
        return DENY_DECODE;
    }
    for (char *p = body; *p; ++p) {
        *p = (char)tolower((unsigned char)*p);
    }
    char *mnemonic;
    char *operands = body;
    do {
        mnemonic = trim(operands);
        operands = mnemonic;
        while (*operands && !isspace((unsigned char)*operands)) {
            ++operands;
        }
        if (*operands) {
            *operands++ = '\0';
        }
    } while (prefix(mnemonic));
    if (!isalpha((unsigned char)*mnemonic)) {
        return DENY_DECODE;
    }
    for (const char *p = mnemonic; *p; ++p) {
        if (!word_char((unsigned char)*p) && *p != '.') {
            return DENY_DECODE;
        }
    }
    if (strlen(mnemonic) == 2 && isxdigit((unsigned char)mnemonic[0]) && isxdigit((unsigned char)mnemonic[1])) {
        return DENY_DECODE;
    }

    /* Only the x86-64 syscall ABI is admitted. Do not allow compatibility-mode
     * transitions to give the syscall callback a different numbering scheme. */
    static const char *const control[] = {
        "int", "int3", "into", "sysenter", "sysexit", "lcall", "lcalll", "lcallq",
        "ljmp", "ljmpl", "ljmpq", "lret", "lretl", "lretq", "retf", "retfq",
        "iret", "iretd", "iretq", "iretl", "sysret", "sysretl", "sysretq",
    };
    if (member(mnemonic, control, ARRAY_LENGTH(control))) {
        return DENY_CONTROL;
    }

    static const char *const non_avx_v[] = {
        "verr", "verw", "vmcall", "vmclear", "vmfunc", "vmlaunch", "vmptrld", "vmptrst",
        "vmread", "vmresume", "vmwrite", "vmxoff", "vmxon", "vmrun", "vmmcall", "vmload", "vmsave",
    };
    bool avx = mnemonic[0] == 'v' && !member(mnemonic, non_avx_v, ARRAY_LENGTH(non_avx_v));
    bool xmm = false, other_vector = false;
    static const char *const registers[] = { "xmm", "ymm", "zmm", "mm", "k", "tmm" };
    for (const char *p = operands; *p; ++p) {
        if (p != operands && word_char((unsigned char)p[-1])) {
            continue;
        }
        for (size_t i = 0; i < ARRAY_LENGTH(registers); ++i) {
            size_t length = strlen(registers[i]);
            if (strncmp(p, registers[i], length) != 0 || !isdigit((unsigned char)p[length])) {
                continue;
            }
            const char *end = p + length;
            while (isdigit((unsigned char)*end)) {
                ++end;
            }
            if (word_char((unsigned char)*end)) {
                continue;
            }
            xmm |= i == 0;
            other_vector |= i != 0;
            avx |= i == 1 || i == 2 || i == 4;
        }
    }
    if (avx && (deny_avx || deny_simd)) {
        return deny_avx ? DENY_AVX : DENY_SIMD;
    }
    if (deny_simd) {
        static const char *const vector_state[] = { "emms", "femms", "ldtilecfg", "sttilecfg", "tilerelease" };
        if (other_vector || (xmm && !scalar_xmm(mnemonic, operands)) ||
            member(mnemonic, vector_state, ARRAY_LENGTH(vector_state))) {
            return DENY_SIMD;
        }
    }
    return ALLOW;
}

static void terminate(const char *message, char report)
{
    /* No guest signal handler or atexit hook gets control after a violation. */
    ssize_t written;
    if (report_ready) {
        do {
            written = write(report_fd, &report, 1);
        } while (written < 0 && errno == EINTR);
    }
    do {
        written = write(STDERR_FILENO, message, strlen(message));
    } while (written < 0 && errno == EINTR);
    /* Reporting is best effort; any other write failure still stops the guest. */
    _exit(126);
}

static void instruction_executed(unsigned int cpu, void *data)
{
    (void)cpu;
    switch ((enum decision)(uintptr_t)data) {
    case DENY_AVX: terminate("cpu-policy: disallowed avx instruction\n", 'V'); break;
    case DENY_SIMD: terminate("cpu-policy: disallowed simd instruction\n", 'V'); break;
    case DENY_CONTROL: terminate("cpu-policy: disallowed execution mode transition\n", 'V'); break;
    default: terminate("cpu-policy: disallowed uninspectable instruction\n", 'V'); break;
    }
}

static void execution_started(unsigned int cpu, void *data)
{
    (void)data;
    if (cpu != 0) {
        terminate("cpu-policy: disallowed additional guest CPU\n", 'S');
    }
    if (!report_ready) {
        if (write(report_fd, "R", 1) != 1) {
            terminate("cpu-policy: cannot initialize trusted report\n", 'S');
        }
        report_ready = true;
    }
}

static void translated(qemu_plugin_id_t id, struct qemu_plugin_tb *tb)
{
    (void)id;
    /* A vCPU exists before ELF loading has completed. Report readiness only
     * when a translated guest block actually begins execution. */
    if (!report_ready) {
        qemu_plugin_register_vcpu_tb_exec_cb(tb, execution_started,
            QEMU_PLUGIN_CB_NO_REGS, NULL);
    }
    size_t count = qemu_plugin_tb_n_insns(tb);
    for (size_t i = 0; i < count; ++i) {
        struct qemu_plugin_insn *insn = qemu_plugin_tb_get_insn(tb, i);
        char *disassembly = qemu_plugin_insn_disas(insn);
        enum decision decision = classify(disassembly);
        g_free(disassembly);
        if (decision != ALLOW) {
            qemu_plugin_register_vcpu_insn_exec_cb(insn, instruction_executed,
                QEMU_PLUGIN_CB_NO_REGS, (void *)(uintptr_t)decision);
        }
    }
}

static bool guest_range(uint64_t address, uint64_t length)
{
    return address < GUEST_ADDRESS_LIMIT && length <= GUEST_ADDRESS_LIMIT - address;
}

/* Numbers are the guest Linux x86-64 ABI, independent of the host ABI. The
 * default deny covers execve/execveat, clone/fork, files, sockets, descriptors
 * belonging to the emulator, /proc/self/mem, and alternate syscall ABIs. */
static bool syscall_allowed(int64_t number, uint64_t a1, uint64_t a2, uint64_t a3,
    uint64_t a4, uint64_t a5, uint64_t a6)
{
    (void)a6;
    switch (number) {
    case 0: case 1: case 3: case 5: case 8: /* read, write, close, fstat, lseek */
    case 17: case 18: case 19: case 20: /* pread64, pwrite64, readv, writev */
        return a1 <= 2;
    case 16: /* ioctl: read-only terminal queries used by stdio */
        return a1 <= 2 && (a2 == 0x5401 || a2 == 0x5413 || a2 == 0x541b);
    case 72: /* fcntl: read-only flags, no duplication or descriptor mutation */
        return a1 <= 2 && (a2 == 1 || a2 == 3);
    case 9: /* mmap: anonymous private guest mappings only */
        return guest_range(a1, a2) && (a4 & 0x20) && (a4 & 3) == 2 && (uint32_t)a5 == UINT32_MAX;
    case 10: case 11: case 28: /* mprotect, munmap, madvise */
        return guest_range(a1, a2);
    case 12: /* brk */
        return a1 < GUEST_ADDRESS_LIMIT;
    case 25: /* mremap */
        return guest_range(a1, a2) && a3 <= GUEST_ADDRESS_LIMIT &&
            (!(a4 & 2) || guest_range(a5, a3));
    case 158: /* arch_prctl: TLS bases and their queries only */
        return (a1 == 0x1001 || a1 == 0x1002 || a1 == 0x1003 || a1 == 0x1004) &&
            guest_range(a2, 8);
    case 202: /* futex: exclude obsolete FUTEX_FD */
        return (a2 & 0x7f) != 2;
    case 302: /* prlimit64: query this process, never change limits */
        return a1 == 0 && a3 == 0;
    case 13: case 14: case 15: /* rt_sigaction, rt_sigprocmask, rt_sigreturn */
    case 4: case 6: case 21: case 89: /* stat/lstat/access/readlink metadata */
    case 262: case 267: case 269: case 332: case 439: /* *at metadata, statx */
    case 24: case 34: case 35: /* sched_yield, pause, nanosleep */
    case 39: case 60: /* getpid, exit */
    case 63: /* uname */
    case 96: case 97: case 98: case 99: case 100: /* time/resource queries */
    case 102: case 104: case 107: case 108: case 110: /* identity queries */
    case 127: case 128: case 130: case 131: /* signal queries/wait/altstack */
    case 186: case 201: /* gettid, time */
    case 218: /* set_tid_address */
    case 228: case 229: case 230: case 231: /* clock queries/sleep, exit_group */
    case 273: /* set_robust_list (single-thread libc startup) */
    case 309: case 318: case 334: /* getcpu, getrandom, rseq */
        return true;
    default:
        return false;
    }
}

static void guest_syscall(qemu_plugin_id_t id, unsigned int cpu, int64_t number,
    uint64_t a1, uint64_t a2, uint64_t a3, uint64_t a4, uint64_t a5, uint64_t a6,
    uint64_t a7, uint64_t a8)
{
    (void)id; (void)cpu; (void)a7; (void)a8;
    if (!syscall_allowed(number, a1, a2, a3, a4, a5, a6)) {
        char diagnostic[96];
        (void)snprintf(diagnostic, sizeof(diagnostic),
            "cpu-policy: disallowed syscall %" PRId64 "\n", number);
        terminate(diagnostic, 'S');
    }
}

QEMU_PLUGIN_EXPORT int qemu_plugin_install(qemu_plugin_id_t id,
    const qemu_info_t *info, int argc, char **argv)
{
    if (!info || info->system_emulation || !info->target_name || strcmp(info->target_name, "x86_64") != 0 ||
        info->version.min > qemu_plugin_version || info->version.cur < qemu_plugin_version || argc < 2 || argc > 3) {
        (void)fputs("cpu-policy: expected x86-64 user emulation and avx/simd policy\n", stderr);
        return -1;
    }
    for (int i = 0; i < argc; ++i) {
        /* QEMU's option parser normalizes a bare name to name=on. Spell that
         * form explicitly in the launcher to avoid its deprecation warning. */
        if ((strcmp(argv[i], "avx") == 0 || strcmp(argv[i], "avx=on") == 0) && !deny_avx) {
            deny_avx = true;
        } else if ((strcmp(argv[i], "simd") == 0 || strcmp(argv[i], "simd=on") == 0) && !deny_simd) {
            deny_simd = true;
        } else if (strncmp(argv[i], "report-fd=", 10) == 0 && report_fd < 0) {
            const char *value = argv[i] + 10;
            char *end;
            errno = 0;
            long fd = strtol(value, &end, 10);
            if (!isdigit((unsigned char)*value) || *end || errno || fd < 3 || fd > INT_MAX) {
                (void)fputs("cpu-policy: invalid trusted report descriptor\n", stderr);
                return -1;
            }
            report_fd = (int)fd;
        } else {
            (void)fputs("cpu-policy: invalid or duplicate policy\n", stderr);
            return -1;
        }
    }
    struct stat report;
    int flags = report_fd < 0 ? -1 : fcntl(report_fd, F_GETFL);
    if ((!deny_avx && !deny_simd) || flags < 0 || (flags & O_ACCMODE) == O_RDONLY ||
        fstat(report_fd, &report) != 0 || !S_ISREG(report.st_mode) || report.st_size != 0 ||
        lseek(report_fd, 0, SEEK_SET) != 0) {
        (void)fputs("cpu-policy: requires an empty writable regular report file and avx/simd policy\n", stderr);
        return -1;
    }
    qemu_plugin_register_vcpu_tb_trans_cb(id, translated);
    qemu_plugin_register_vcpu_syscall_cb(id, guest_syscall);
    return 0;
}
