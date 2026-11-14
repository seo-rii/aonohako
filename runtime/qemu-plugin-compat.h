/*
 * Minimal declarations from QEMU's public plugin ABI, versions 7.2 and 10.0.
 * https://github.com/qemu/qemu/blob/v10.0.0/include/qemu/qemu-plugin.h
 * Copyright (C) 2017, Emilio G. Cota <cota@braap.org>
 * Copyright (C) 2019, Linaro
 * SPDX-License-Identifier: GPL-2.0-or-later
 *
 * Only the ABI shared by these versions is used. In particular, the versioned
 * qemu_plugin_insn_data API is deliberately absent. Production targets API 4;
 * API 1 is an explicit build-time option for the older local test emulator.
 */
#ifndef AONOHAKO_QEMU_PLUGIN_COMPAT_H
#define AONOHAKO_QEMU_PLUGIN_COMPAT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifndef AONOHAKO_QEMU_PLUGIN_API_VERSION
#define AONOHAKO_QEMU_PLUGIN_API_VERSION 4
#endif

#define QEMU_PLUGIN_EXPORT __attribute__((visibility("default")))

typedef uint64_t qemu_plugin_id_t;
typedef struct qemu_info_t {
    const char *target_name;
    struct { int min; int cur; } version;
    bool system_emulation;
    union { struct { int smp_vcpus; int max_vcpus; } system; };
} qemu_info_t;

struct qemu_plugin_tb;
struct qemu_plugin_insn;
enum qemu_plugin_cb_flags {
    QEMU_PLUGIN_CB_NO_REGS,
    QEMU_PLUGIN_CB_R_REGS,
    QEMU_PLUGIN_CB_RW_REGS,
};

typedef void (*qemu_plugin_vcpu_udata_cb_t)(unsigned int, void *);
typedef void (*qemu_plugin_vcpu_tb_trans_cb_t)(qemu_plugin_id_t, struct qemu_plugin_tb *);
typedef void (*qemu_plugin_vcpu_syscall_cb_t)(qemu_plugin_id_t, unsigned int,
    int64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t, uint64_t,
    uint64_t, uint64_t);

void qemu_plugin_register_vcpu_tb_trans_cb(qemu_plugin_id_t, qemu_plugin_vcpu_tb_trans_cb_t);
size_t qemu_plugin_tb_n_insns(const struct qemu_plugin_tb *);
struct qemu_plugin_insn *qemu_plugin_tb_get_insn(const struct qemu_plugin_tb *, size_t);
char *qemu_plugin_insn_disas(const struct qemu_plugin_insn *);
void qemu_plugin_register_vcpu_insn_exec_cb(struct qemu_plugin_insn *,
    qemu_plugin_vcpu_udata_cb_t, enum qemu_plugin_cb_flags, void *);
void qemu_plugin_register_vcpu_tb_exec_cb(struct qemu_plugin_tb *,
    qemu_plugin_vcpu_udata_cb_t, enum qemu_plugin_cb_flags, void *);
void qemu_plugin_register_vcpu_syscall_cb(qemu_plugin_id_t, qemu_plugin_vcpu_syscall_cb_t);

/* qemu_plugin_insn_disas returns GLib-owned memory. QEMU exports GLib symbols. */
void g_free(void *);

#endif
