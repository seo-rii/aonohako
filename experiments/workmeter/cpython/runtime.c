/* Diagnostic counters for trusted single-process, single-threaded workloads.
 * These counters share the target's address space. A valid report means that
 * the checks below passed, not that every CPU operation was measured or that
 * the report is protected from a target program modifying its own state.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

enum {
    COUNTER_OVERFLOW = 1,
    INVALID_REGISTRATION = 2,
    DUPLICATE_REGISTRATION = 4,
    UNSUPPORTED_THREADS = 8,
    TOTAL_OVERFLOW = 16,
    MISSING_METER = 32,
    UNSUPPORTED_FORK = 64,
    SETUP_FAILED = 128,
};

struct module {
    const char *id;
    uint64_t *counts;
    const uint64_t *weights;
    uint64_t n;
};

static struct module modules[8192];
static size_t nmodules;
static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;
static _Atomic unsigned flags;
static char *path, *run_id;
static int expect_meter;
static pid_t original_pid;

static void json_string(FILE *out, const char *value)
{
    fputc('"', out);
    if (value) {
        for (; *value; value++) {
            unsigned char c = *value;
            if (c == '"' || c == '\\') {
                fputc('\\', out);
                fputc(c, out);
            } else if (c < 32) {
                fprintf(out, "\\u%04x", c);
            } else {
                fputc(c, out);
            }
        }
    }
    fputc('"', out);
}

void __wm_overflow(void)
{
    atomic_fetch_or(&flags, COUNTER_OVERFLOW);
}

void __wm_register(const char *id, uint64_t *counts,
                   const uint64_t *weights, uint64_t n)
{
    pthread_mutex_lock(&lock);
    if (nmodules >= 8192 || !id || !counts || !weights || !n) {
        atomic_fetch_or(&flags, INVALID_REGISTRATION);
    } else {
        for (size_t i = 0; i < nmodules; i++) {
            if (modules[i].counts == counts) {
                atomic_fetch_or(&flags, DUPLICATE_REGISTRATION);
                pthread_mutex_unlock(&lock);
                return;
            }
        }
        modules[nmodules++] = (struct module){id, counts, weights, n};
    }
    pthread_mutex_unlock(&lock);
}

/* Counter increments deliberately remain non-atomic. Reject thread creation
 * before another thread can execute instrumented code; a flag alone would not
 * prevent undefined behavior from counter data races. */
int pthread_create(pthread_t *thread, const pthread_attr_t *attr,
                   void *(*function)(void *), void *arg)
{
    (void)thread;
    (void)attr;
    (void)function;
    (void)arg;
    atomic_fetch_or(&flags, UNSUPPORTED_THREADS);
    return ENOTSUP;
}

static void before_fork(void)
{
    /* Parent counters do not include work performed in another process. */
    atomic_fetch_or(&flags, UNSUPPORTED_FORK);
}

static void report(void)
{
    /* The child must not race the parent's exclusive report pathname. */
    if (!path || getpid() != original_pid) {
        return;
    }
    unsigned failed = atomic_load(&flags);
    __uint128_t total = 0;
    uint64_t nonzero = 0;
    if (!failed) {
        for (size_t i = 0; i < nmodules; i++) {
            for (uint64_t j = 0; j < modules[i].n; j++) {
                uint64_t count = modules[i].counts[j];
                if (count) {
                    nonzero++;
                    total += (__uint128_t)count * modules[i].weights[j];
                    if (total > UINT64_MAX) {
                        failed |= TOTAL_OVERFLOW;
                    }
                }
            }
        }
    }
    if (expect_meter && !nmodules) {
        failed |= MISSING_METER;
    }

    int fd = open(path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0600);
    if (fd < 0) {
        return;
    }
    FILE *out = fdopen(fd, "w");
    if (!out) {
        close(fd);
        return;
    }
    fprintf(out, "{\"schema\":1,\"pid\":%ld,\"valid\":%s,"
                 "\"metric\":\"llvm_ir_block_work\",\"work_units\":",
            (long)getpid(), failed ? "false" : "true");
    if (failed) {
        fputs("null", out);
    } else {
        fprintf(out, "%" PRIu64, (uint64_t)total);
    }
    fprintf(out, ",\"flags\":%u,\"reason\":", failed);
    json_string(out, failed ? "counter_integrity_or_unsupported_process_model" : "ok");
    fputs(",\"run_id\":", out);
    json_string(out, run_id);
    fprintf(out, ",\"nonzero_blocks\":%" PRIu64 ",\"modules_seen\":%zu,"
                 "\"scope\":\"compiled_CPython_core_and_built_stdmods\","
                 "\"validity_scope\":\"counter_integrity_only\","
                 "\"authority\":\"diagnostic_only\","
                 "\"model_version\":\"llvm-ir-v1\","
                 "\"gaps\":[\"external_shared_libraries\",\"kernel\","
                 "\"assembly_internal_work\",\"variable_size_memory_intrinsics\","
                 "\"unobserved_process_creation\"],"
                 "\"vector_semantics\":\"sparse_per_module_block_visits; "
                 "omitted_indexes_zero; metadata_id_MD5_of_static_metadata\","
                 "\"modules\":[", nonzero, nmodules);
    for (size_t i = 0; i < nmodules; i++) {
        if (i) {
            fputc(',', out);
        }
        fputs("{\"metadata_id\":", out);
        json_string(out, modules[i].id);
        fprintf(out, ",\"blocks\":%" PRIu64 ",\"counts\":[", modules[i].n);
        int first = 1;
        if (!failed) {
            for (uint64_t j = 0; j < modules[i].n; j++) {
                if (modules[i].counts[j]) {
                    if (!first) {
                        fputc(',', out);
                    }
                    first = 0;
                    fprintf(out, "[%" PRIu64 ",%" PRIu64 "]", j, modules[i].counts[j]);
                }
            }
        }
        fputs("]}", out);
    }
    fputs("]}\n", out);
    if (fclose(out) != 0) {
        unlink(path);
    }
}

__attribute__((constructor)) static void setup(void)
{
    const char *report_path = getenv("WM_REPORT_PATH");
    const char *requested_id = getenv("WM_RUN_ID");
    const char *expected = getenv("WM_EXPECT_METER");
    original_pid = getpid();
    if (report_path) {
        path = strdup(report_path);
    }
    if (requested_id) {
        run_id = strdup(requested_id);
    }
    expect_meter = expected && strcmp(expected, "1") == 0;
    if ((report_path && !path) || (requested_id && !run_id) ||
        pthread_atfork(before_fork, NULL, NULL) != 0 || atexit(report) != 0) {
        atomic_fetch_or(&flags, SETUP_FAILED);
    }
}
