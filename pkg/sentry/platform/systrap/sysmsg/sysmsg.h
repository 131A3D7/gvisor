// Copyright 2020 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#ifndef THIRD_PARTY_GVISOR_PKG_SENTRY_PLATFORM_SYSTRAP_SYSMSG_SYSMSG_H_
#define THIRD_PARTY_GVISOR_PKG_SENTRY_PLATFORM_SYSTRAP_SYSMSG_SYSMSG_H_

#include <stdint.h>
#include <sys/user.h>

#include "sysmsg_offsets.h"  // NOLINT

#if defined(__x86_64__)
// LINT.IfChange
struct arch_state {
  uint32_t xsave_mode;
  uint32_t fp_len;
  uint32_t fsgsbase;
};
// LINT.ThenChange(sysmsg_amd64.go)
#else
// LINT.IfChange
struct arch_state {};
// LINT.ThenChange(sysmsg_arm64.go)
#endif

// LINT.IfChange
enum {
  SYSMSG_STATE_NONE,
  SYSMSG_STATE_DONE,
  SYSMSG_STATE_EVENT,
  SYSMSG_STATE_SIGACT,
  SYSMSG_STATE_PREP,
};

enum sysmsg_type {
  SYSMSG_NONE,
  SYSMSG_SYSCALL,
  SYSMSG_FAULT,
  SYSMSG_SYSCALL_TRAP,
  SYSMSG_SYSCALL_NEED_TRAP,
  SYSMSG_INTERRUPT,
};

// sysmsg contains the current state of the sysmsg thread. See: sysmsg.go:Msg
struct sysmsg {
  struct sysmsg *self;
  uint64_t ret_addr;
  uint64_t syshandler;
  uint64_t syshandler_stack;
  uint64_t app_stack;
  uint32_t interrupt;
  int32_t fault_jump;
  uint32_t type;
  uint32_t state;
  uint64_t context_id;
  uint64_t context_region;

  // The fields above have offsets defined in sysmsg_offsets*.h

  int32_t signo;
  int32_t err;
  int32_t err_line;
  uint64_t debug;
  struct user_regs_struct ptregs;
  uint64_t fpstate;
  siginfo_t siginfo;
  // tls is only populated on ARM64.
  uint64_t tls;
  uint32_t stub_fast_path;
  uint32_t sentry_fast_path;
  uint32_t acked_events;
  uint64_t interrupted_context_id;
};

// thread_context contains the current context of the sysmsg thread.
// See sysmsg.go:SysThreadContext
struct thread_context {
  uint8_t fpstate[MAX_FPSTATE_LEN];
  uint64_t fpstate_changed;
  struct user_regs_struct ptregs;

  // The fields above have offsets defined in sysmsg_offsets*.h

  siginfo_t siginfo;
  int64_t signo;
  uint64_t interrupt;
};

#ifndef PAGE_SIZE
#define PAGE_SIZE 4096
#endif
#define PER_THREAD_MEM_SIZE (8 * PAGE_SIZE)
#define GUARD_SIZE (PAGE_SIZE)
#define MSG_OFFSET_FROM_START (PER_THREAD_MEM_SIZE - PAGE_SIZE)

// LINT.ThenChange(sysmsg.go)

#define FAULT_OPCODE 0x06  // "push %es" on x32 and invalid opcode on x64.

#define __stringify_1(x...) #x
#define __stringify(x...) __stringify_1(x)
#define BUILD_BUG_ON(condition) ((void)sizeof(char[1 - 2 * !!(condition)]))

extern uint64_t __export_pr_sched_core;
extern uint64_t __export_deep_sleep_timeout;
extern struct arch_state __export_arch_state;
extern uint64_t __export_context_decoupling_exp;

// NOLINTBEGIN(runtime/int)
static void *sysmsg_sp() {
  volatile int p;
  void *sp =
      (struct sysmsg *)(((long)&p) / PER_THREAD_MEM_SIZE * PER_THREAD_MEM_SIZE);

  _Static_assert(
      sizeof(struct sysmsg) < (PER_THREAD_MEM_SIZE - MSG_OFFSET_FROM_START),
      "The sysmsg structure is too big.");
  return sp;
}

static struct sysmsg *sysmsg_addr(void *sp) {
  return (struct sysmsg *)(sp + MSG_OFFSET_FROM_START);
}

long __syscall(long n, long a1, long a2, long a3, long a4, long a5, long a6);

struct __kernel_timespec;
long sys_futex(uint32_t *addr, int op, int val, struct __kernel_timespec *tv,
               uint32_t *addr2, int val3);

static void __panic(int err, long line) {
  void *sp = sysmsg_sp();
  struct sysmsg *sysmsg = sysmsg_addr(sp);
  sysmsg->err = err;
  sysmsg->err_line = line;
  __atomic_store_n(&sysmsg->state, SYSMSG_STATE_EVENT, __ATOMIC_RELEASE);
  sys_futex(&sysmsg->state, FUTEX_WAKE, 1, NULL, NULL, 666);
  // crash the stub process.
  //
  // Normal user processes cannot map addresses lower than vm.mmap_min_addr
  // which is usually > 4K. So writing to an address <4K should crash the
  // process with a segfault.
  *(int *)(line % 4096) = err;
}

int wait_state(struct sysmsg *sysmsg, uint32_t state);

#define panic(err) __panic(err, __LINE__)
// NOLINTEND(runtime/int)

#endif  // THIRD_PARTY_GVISOR_PKG_SENTRY_PLATFORM_SYSTRAP_SYSMSG_SYSMSG_H_
