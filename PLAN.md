# aonohako Large Work Plan

이 문서는 `RISK_REGISTER.md`에 정리된 보안/운영 리스크 중 큰 구조 변경이 필요한 작업을 구현 순서대로 정리한다.
짧은 방어 패치는 이미 여러 커밋으로 일부 처리됐고, 여기서는 backend 경계, kernel isolation, 운영 quota, runtime image, regression 체계처럼 별도 설계와 단계적 rollout이 필요한 작업만 다룬다.

- 기준 브랜치: `main`
- 작성일: 2026-04-24
- 기준 커밋: `c853208 fix: stabilize dotnet sandbox smoke`

## 최근 완료된 단기 배치

아래 항목은 이미 `main`에 커밋/푸시된 단기 개선이다. `RISK_REGISTER.md`는 workspace 규칙상 커밋하지 않는 추적 문서이므로, 상태 정리는 별도 로컬 추적 작업으로 남긴다.

- inbound bearer auth, bounded pending queue default, `stdin`/`expected_stdout` field cap
- SPJ executable/temp file 권한 수정, SPJ clean cwd 분리, SPJ 전용 limits
- remote SSE line/event/stream cap, remote HTTP transport timeout
- stdout/stderr truncation flag, sidecar diagnostics, image stream cap
- workspace file count/depth cap, `.NET` finite file-size override, benign `..` filename 허용
- `execveat` seccomp 차단 regression
- compile threat model 문서화, `govulncheck` CI job 추가

## 최근 완료된 장기 Phase 조각

아래 항목은 장기 phase 중 작게 나눠 이미 `main`에 커밋/푸시한 진행분이다.

- Phase 9: SPJ timeout/init failure 같은 judge-owned failure reason을 최종
  `RunResponse.reason`에 보존해 contestant failure와 구분되게 했다.
- Phase 10: remote SSE line/event/stream cap에 더해 idle heartbeat timeout을
  추가하고 `AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC`로 config화했다.
- Phase 10: `X-Aonohako-Protocol-Version` 응답 헤더를 추가하고, remote
  runner가 누락된 헤더는 backward-compatible로 허용하되 mismatched header는
  fail-closed 처리하게 했다.
- Phase 10: malformed remote `log`, `image`, `error`, `result` event를
  protocol error로 실패 처리해 compromised/misconfigured runner stream을
  조용히 무시하지 않게 했다.
- Phase 11: post-start `execve()` image surface를 architecture/security
  contract의 explicit gap으로 기록하고 docs contract test로 고정했다.
- Phase 11: workspace entry-count exhaustion과 depth exhaustion을 실제
  root-backed sandbox security suite probe로 추가했다.
- Phase 11: procfs regression coverage에 `/proc/1/fd` browsing뿐 아니라
  `/proc/1/environ` read 차단 probe를 추가했다.
- Phase 12: `aonohako-selftest deployment-contract`를 추가해 현재 env가 어떤
  security contract와 guardrail로 해석되는지 token 없이 JSON으로 확인할 수
  있게 했다.
- Phase 12: production 계열 target에서 inbound auth `none`과 remote runner
  auth `none`을 reject하도록 startup validation을 강화했다.
- Phase 2: cgroup v2 preflight, parent controller enable, per-run group
  create/add-proc/remove primitive를 추가하고 architecture docs contract로
  고정했다.
- Phase 2: cgroup accounting reader가 `memory.events`, `pids.events`,
  `cpu.stat`의 limit signal을 노출해 future verdict reason이 RSS polling에만
  의존하지 않게 했다.
- Phase 11: execute sandbox suite에 filesystem metadata syscall과 BPF,
  userfaultfd, io_uring, perf-event probe를 추가해 denylist regression
  coverage를 넓혔다.
- Phase 18: compile sandbox suite에도 동일한 kernel attack surface syscall
  probe를 추가해 `/compile`이 적대 입력 실행면임을 테스트로 고정했다.
- Phase 11/18: `memfd_create`를 기본 seccomp deny 대상으로 추가하고,
  CoreCLR/Wasmtime compatibility 때문에 .NET과 Wasmtime helper request에서만
  예외로 남겼다.
  execute/compile kernel attack surface probe가 이 syscall 차단도 확인한다.
- Phase 12: CI에서 sandbox regression용 Python runtime image의 Syft SBOM
  artifact를 생성하도록 supply-chain 추적성을 추가했다.
- Phase 12: 같은 runtime image에 대해 non-blocking Grype JSON scan artifact를
  생성해 CVE drift를 관찰할 수 있게 했다. promotion gate는 full production
  matrix 기준으로 남아 있다.
- Phase 12: CI workflow에 same-ref concurrency cancellation을 추가해 연속
  보안 배치 푸시가 오래된 runtime image build와 scan job을 계속 쌓지 않게
  했다.
- Phase 6: runtime Dockerfile builder/runtime base와 runtime catalog production
  profile base image를 digest-pinned reference로 바꾸고 repo tests로 고정했다.
- Phase 6: runtime image hardening에서 package manager와 fetcher binaries
  (`apt`, `dpkg`, `curl`, `wget`)를 root-only execute로 잠가 sandbox UID가
  post-start `execve()` 표면으로 쓰지 못하게 했다.
- Phase 2: per-run cgroup 생성 시 `memory.oom.group=1`을 설정해 future
  child-process mode에서 run 전체가 하나의 OOM domain으로 취급되게 했다.
- Phase 12: CI `govulncheck`가 취약한 Go 1.26.0 표준 라이브러리를 쓰지
  않도록 module Go directive를 1.26.2로 올렸다.
- Phase 8/10: `.NET` sandbox smoke를 안정화했다. CoreCLR의 memfd-backed
  double-mapper 때문에 `dotnet`은 address-space/file-size rlimit을 끄되,
  `/tmp/.dotnet` shared state를 sandbox UID로 매 실행 전 재생성하고 compile
  failure signal reason과 SSE heartbeat shutdown을 보강했다.
- Phase 2: `AONOHAKO_CGROUP_PARENT`를 self-hosted embedded helper 전용
  opt-in으로 추가해 compile/execute/SPJ helper process를 per-run cgroup에
  넣고 `memory.max`, `pids.max`, `memory.oom.group=1` 이벤트를 watchdog에
  연결했다. Cloud Run helper mode는 계속 cgroup 비의존 경로로 남는다.
- Phase 2: self-hosted cgroup opt-in의 compile/execute/SPJ run group에
  `cpu.max=100000 100000`을 연결해 run 단위 CPU bandwidth도 1 vCPU로
  제한한다. CPU throttling counter는 아직 verdict source가 아니라
  guardrail/diagnostic으로 유지한다.
- Phase 11: runtime image smoke에 `aonohako-selftest runtime-memory`를 추가해
  Node/TypeScript runtime memory guard, Wasmtime constrained-memory trap,
  .NET GC/RSS guardrail이 언어 이미지별 CI에서 정상 완료나 timeout으로
  새지 않게 했다.
- Phase 12: Syft/Grype supply-chain CI를 action 내장 downloader 대신
  checksum 검증과 retry가 있는 로컬 설치 스크립트로 전환해 GitHub release
  asset의 일시적 5xx에 덜 민감하게 했다.
- Phase 10/12: unsigned platform trusted-header mode에
  `AONOHAKO_PLATFORM_TRUSTED_PROXY_CIDRS`를 추가하고, source CIDR와
  `X-Aonohako-Principal` 존재를 API layer에서 확인하게 했다.
- Phase 11/18: execute/compile seccomp regression probe가 modern mount API
  syscall과 pidfd syscall 차단까지 직접 확인하게 해 denylist drift 감지를
  넓혔다.
- Phase 6: runtime image hardening의 root-only executable fetcher 목록에
  `git`을 추가하고 image-permission selftest와 docs contract에 반영했다.
- Phase 11: execute watchdog regression에 mmap-backed RSS spike 케이스를
  추가해 statm/smaps 기반 MLE 감지를 고정했다.
- Phase 11: procfs regression에 `/proc/1/root`, `/proc/1/cwd`, `/proc/1/exe`
  symlink 및 `/proc/1/root/etc/passwd` read attempt를 추가했다.
- Phase 11: runtime memory smoke에 Go 기반 `uhmlang` interpreter stress를
  추가해 `GOMEMLIMIT`/RSS guardrail 회귀를 언어 이미지에서 확인하게 했다.
- Phase 11: runtime memory smoke에 Java heap pressure 케이스를 추가해 JVM
  `-Xmx`/watchdog 조합이 Accepted/TLE로 새지 않게 고정했다.
- Phase 11: runtime memory smoke에 Python/PyPy heap pressure 케이스를
  추가해 기본 `RLIMIT_AS`/RSS watchdog 경로를 언어 이미지에서 확인하게 했다.
- Phase 11: runtime memory smoke에 Groovy/Scala/Clojure heap pressure
  케이스를 추가해 JVM classpath/launcher별 `-Xmx` 회귀를 확인하게 했다.
- Phase 11: Cloud Run runner/control-plane, self-hosted runner, dev
  control-plane `.env` 예시와 docscontract lint를 추가해 auth, ingress,
  single-slot 배포 계약 drift를 문서 단계에서 잡게 했다.
- Phase 2: execute watchdog이 cgroup `cpu.stat usage_usec`를 읽어 self-hosted
  cgroup opt-in에서 run cgroup 전체 CPU 사용량으로 `cpu_time_ms`와 CPU TLE를
  갱신하게 했다.
- Phase 2: compile/execute watchdog의 cgroup `memory.events`, `pids.events`,
  `memory.current` limit signal 해석을 shared helper로 묶어 두 경로의 verdict
  drift를 줄이고 unit test로 고정했다.
- Phase 6: runtime image hardening의 root-only executable 목록을 build-time
  package/toolchain manager(`pip`, `npm`, `cargo`, `rustup`, `gem` 등)까지
  확장하고 image-permission selftest와 docs contract에 반영했다.
- Phase 12: production runtime profile docker archive artifact마다 SHA256
  sidecar를 만들고 summary bundle에서 다운로드된 archive 기준 `SHA256SUMS`를
  재계산해 promotion 입력 artifact 무결성 metadata를 남기게 했다.
- Phase 11: runtime memory smoke에 lightweight `plain` C CPU loop TLE case를
  추가해 runtime image smoke가 CPU-time verdict path도 검증하게 했다.
- Phase 2/18: execute 전용 workspace scanner를 shared package로 옮기고 compile
  watchdog에도 연결해 컴파일 중 total bytes, entry count, directory depth
  초과를 같은 quota signal로 차단하게 했다.
- Phase 11/18: execute/compile seccomp regression probe에 file-handle,
  fanotify, keyring, kernel-module, syslog 계열 syscall 확인을 추가해
  denylist drift 감지를 넓혔다.
- Phase 3/18: execute target과 compile helper command가 부모 서버 프로세스의
  token/secret 환경변수를 상속하지 않고 fixed base/workspace/runtime env만
  받는지 regression으로 고정했다.
- Phase 12: production runtime profile matrix도 Syft SBOM과 non-blocking Grype
  JSON scan artifact를 각 profile artifact에 포함하게 해 full production
  matrix의 CVE drift 추적성을 높였다.
- Phase 7/18: `/compile` source count와 decoded-size budget을 API layer에서
  stream/queue slot 획득 전에 검증해 oversized compile 요청이 runner queue에
  들어가지 않게 했다.
- Phase 7: per-principal request-rate window가 만료된 뒤에도 map에 무기한
  남지 않도록 stale window cleanup을 추가했다.
- Phase 11: runtime memory smoke의 `plain` profile에 native C heap pressure
  케이스를 추가해 기본 `RLIMIT_AS`/RSS watchdog 경로도 이미지 smoke에서
  확인하게 했다.
- Phase 7/12: non-dev `platform` inbound auth에서 unsigned trusted-header
  모드를 제거하고 `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET` 서명을 startup
  requirement로 만들었다.
- Phase 6: runtime image hardening의 root-only executable 목록을
  remote-access, debugger, network-diagnostic tool(`ssh`, `rsync`, `gdb`,
  `strace`, `tcpdump`, `nmap`, `dig`, `ip`, `ping` 등)까지 확장하고
  image-permission selftest와 docs contract에 반영했다.
- Phase 3/11: 서버와 selftest parent process도 startup에서
  `PR_SET_DUMPABLE=0`를 설정해 sandbox UID의 same-container procfs/ptrace
  style 접근 표면을 줄였다.
- Phase 12: production runtime profile artifact summary job에 fail-closed
  verifier를 추가해 SBOM JSON, Grype JSON, summary, docker archive, archive
  SHA256 sidecar, consolidated `SHA256SUMS`가 누락되거나 digest mismatch일 때
  summary bundle 업로드 전에 실패하게 했다.
- Phase 11/18: execute/compile seccomp regression probe와 helper denylist에
  `lookup_dcookie`, kexec, NFS server control, quota control,
  `process_madvise`, `process_mrelease`, reboot/swap 계열 syscall을 보강했다.
- Phase 9/CONF: `AONOHAKO_RUNTIME_TUNING_PROFILES`를 추가해 운영자가 미리
  등록한 bounded numeric tuning profile을 `/compile`과 `/execute`의
  `runtime_profile`로 선택할 수 있게 했다. 임의 runtime flag 전달은 계속
  허용하지 않는다.
- Phase 9/CONF: `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE`을 추가해 non-dev
  서버에서는 request-supplied `runtime_profile`을 기본 거부하고, trusted
  control-plane/problem-policy 경계에서 명시적으로 허용한 경우에만 profile을
  적용하게 했다.
- Phase 9/CONF: remote compile/execute runner가 trusted control-plane에서
  붙인 `runtime_profile`을 downstream runner 요청 JSON에 보존하는지 regression
  test로 고정했다.
- Phase 9/CONF: `problem_id`와 `AONOHAKO_PROBLEM_RUNTIME_PROFILES`를 추가해
  서버가 문제 정책으로 bounded runtime profile을 선택할 수 있게 했다. direct
  `runtime_profile` 선택은 non-dev 기본 거부이며, 문제 정책과 충돌하는 profile
  요청은 queue 진입 전에 거부한다.
- Phase 12: production profile SBOM/Grype scan 전에 Docker build cache와
  dangling image를 prune해 Swift 같은 대형 profile에서 Syft의 daemon-export가
  `/tmp` 공간 부족으로 실패하지 않도록 했다.
- Phase 11/18: helper seccomp denylist와 execute/compile regression probe에
  NUMA/mempolicy, `kcmp`, nested `seccomp`, Landlock policy syscall 차단을
  추가해 kernel policy surface drift 감지를 넓혔다.

## 작업 원칙

- 각 phase는 하나 이상의 작고 검증 가능한 커밋으로 나눈다.
- 보안 기능은 가능하면 regression test를 먼저 추가하고 fail-closed 동작을 확인한 뒤 구현한다.
- Cloud Run compatible helper backend는 현재 좁은 운영 모델을 유지하고, self-hosted backend에서 강한 isolation을 추가한다.
- child process 허용, concurrency 증가, root helper 노출 확대는 해당 accounting/isolation phase가 끝나기 전에는 금지한다.
- `RISK_REGISTER.md`는 추적 문서이므로 커밋하지 않는다. 코드/문서 변경이 완료되면 risk 상태 정리는 별도 로컬 follow-up으로 수행한다.

## Phase 0: 기준선 고정과 남은 리스크 분류

대상 리스크: `ROADMAP-001`, `TEST-001`

목표: 지금까지 적용한 단기 수정이 깨지지 않도록 기준 테스트와 보안 계약을 고정한다.

작업:

- 현재 통과 기준을 명시한다: `go test ./...`, `./scripts/check_repo_policy.sh`, `git diff --check`.
- 최근 완료된 단기 항목과 아직 구조 작업이 필요한 항목을 issue id 기준으로 다시 매핑한다.
- Cloud Run helper mode와 self-hosted strong isolation mode의 보안 계약을 표로 분리한다.
- 이후 phase별 PR/커밋 설명에서 동일한 verification template을 재사용한다.

완료 조건:

- 각 남은 risk id가 하나 이상의 phase에 연결된다.
- 새 기능 없이도 현재 보안 계약을 설명하는 문서와 테스트 기준이 명확하다.

## Phase 1: 실행 backend 경계와 배포 topology 정리

대상 리스크: `OPS-001`, `OPS-002`, `ROADMAP-001`, `DOC-001`

목표: public API/control plane과 root helper runner를 명확히 분리하고, backend별 capability를 코드와 문서에서 표현한다.

작업:

- local helper, remote runner, future self-hosted isolated runner를 같은 execution backend contract로 모델링한다.
- backend capability에 `network`, `cgroup`, `mount_namespace`, `masked_proc`, `per_run_uid`, `child_process_accounting` 같은 feature bit를 둔다.
- high-trust deployment에서는 public API가 non-root control plane으로 동작하고 private runner pool만 root helper를 갖도록 문서화한다.
- unsafe concurrency 설정은 startup warning 또는 hard failure 정책을 정한다.
- `/compile`도 untrusted execution surface로 backend contract에 포함한다.

검증:

- existing local/remote compile and execute tests가 동일 contract 아래에서 통과한다.
- unsafe helper concurrency setting에 대한 config test를 추가한다.
- architecture docs가 Cloud Run helper mode와 self-hosted isolated mode를 혼동하지 않는다.

완료 조건:

- 강한 isolation 작업이 backend 구현 세부가 아니라 명시적 capability gap으로 추적된다.
- runner pool 확장은 active run 수 증가가 아니라 instance 수 증가로만 안내된다.

## Phase 2: self-hosted cgroup v2 backend

대상 리스크: `RES-001`, `RES-005`, `JUDGE-002`, `ROADMAP-001`

목표: per-run cgroup으로 memory, pids, CPU, IO accounting을 hard boundary에 가깝게 만든다.

작업:

- self-hosted 전용 cgroup manager를 추가하고 runner startup에서 cgroup v2 availability를 preflight한다.
- run마다 cgroup을 만들고 `memory.max`, `pids.max`, `cpu.max`, 필요 시 `io.max`를 적용한다.
- helper child와 허용된 descendant process가 같은 cgroup에 들어가도록 lifecycle을 관리한다.
- current RSS polling은 fallback/diagnostic으로 남기고 verdict source는 cgroup accounting을 우선한다.
- cgroup OOM, pids limit, CPU throttle, cleanup failure를 별도 internal reason으로 분류한다.

검증:

- memory spike, mmap-heavy allocation, child process memory, fork-like pids pressure regression을 추가한다.
- cgroup cleanup이 success, timeout, crash path에서 모두 실행되는지 확인한다.
- Node, .NET, Wasmtime처럼 address-space limit을 완화한 runtime에 stress tests를 추가한다.

완료 조건:

- child process 허용 여부와 무관하게 run 단위 memory/process accounting이 가능하다.
- Cloud Run helper mode와 self-hosted cgroup mode의 verdict source 차이가 문서화된다.

## Phase 3: mount namespace, read-only rootfs, tmpfs workdir, masked proc

대상 리스크: `ARCH-001`, `ARCH-002`, `SEC-003`, `ROADMAP-001`

목표: self-hosted backend에서 제출물마다 파일시스템과 procfs 관찰 범위를 분리한다.

작업:

- private mount namespace를 만들고 rootfs를 read-only로 remount한다.
- per-run writable workdir을 bounded tmpfs 또는 quota-backed directory로 제공한다.
- `/tmp`, `/var/tmp`, `/dev/shm` 같은 scratch 경로를 run-local mount로 대체해 global chmod toggling을 제거한다.
- `/proc`은 masked proc 또는 private PID namespace + `hidepid` 정책으로 최소화한다.
- runtime별 필요한 read-only bind mount 목록을 allowlist로 정리한다.
- Cloud Run backend에서는 이 phase가 unavailable capability임을 명시한다.

검증:

- `/etc`, `/usr`, `/proc/1`, mount info, global scratch mutation probe를 regression으로 추가한다.
- runtime interpreter가 필요한 파일만 읽을 수 있고 workspace 밖 임의 world-readable path는 실패해야 한다.
- cleanup 후 mount/cgroup/uid residue가 남지 않는지 검사한다.

완료 조건:

- self-hosted isolated backend에서는 제출물이 container root filesystem을 일반 파일처럼 탐색할 수 없다.
- shared scratch directory mode mutation이 helper critical path에서 제거된다.

## Phase 4: per-run UID 또는 user namespace

대상 리스크: `ARCH-001`, `OPS-002`, `ROADMAP-001`

목표: shared sandbox UID 모델을 self-hosted backend에서 per-run identity 모델로 대체한다.

작업:

- UID/GID allocation strategy를 정한다: host UID pool, user namespace mapping, 또는 isolated container backend.
- workspace ownership, artifact capture, SPJ read-only handoff가 per-run UID에서 동작하도록 정리한다.
- concurrent isolated runs가 같은 host 안에서 서로의 files/processes를 관찰하지 못하게 한다.
- UID exhaustion, stale ownership, crash cleanup path를 설계한다.

검증:

- 두 run을 동시에 실행해 cross-run file/process access가 실패하는지 확인한다.
- cleanup 실패 후 다음 run이 stale UID/resource를 재사용하지 않는지 테스트한다.
- SPJ와 sidecar capture가 per-run UID에서도 기존 결과 schema를 유지한다.

완료 조건:

- self-hosted isolated backend에서는 runner instance당 concurrency를 안전하게 늘릴 수 있는 전제가 생긴다.
- Cloud Run helper mode는 계속 single-slot contract로 남는다.

## Phase 5: language-family seccomp allowlist profiles

대상 리스크: `SEC-001`, `SEC-002`, `TEST-001`

목표: denylist 중심 syscall policy를 language-family allowlist profile로 전환할 수 있는 기반을 만든다.

작업:

- native C/C++, Python, JVM, Node, .NET, Wasmtime, Go 계열 profile을 분리한다.
- trace 기반으로 필요한 syscall 목록을 수집하되, profile은 수동 review와 regression으로 고정한다.
- `execve`/`execveat`, file mutation, process creation, namespace, network, kernel feature syscall 정책을 profile별로 명시한다.
- compatibility fallback이 필요한 runtime은 denylist mode로 남기되 startup/runtime warning을 남긴다.
- profile 변경은 CI에서 syscall policy snapshot diff로 review되게 한다.

검증:

- socket, fork/clone process, ptrace, mount, bpf, io_uring, keyctl, userfaultfd, `/bin/sh` exec probe를 profile별로 실행한다.
- 허용 profile에서 정상 hello-world, file IO, runtime startup, SPJ execution이 통과한다.
- 새 kernel syscall이 위험 기능을 열면 regression이 실패해야 한다.

완료 조건:

- public OJ용 profile은 allowlist 기반으로 운영 가능하다.
- denylist fallback은 compatibility exception으로만 남고 기본 보안 모델이 아니다.

## Phase 6: runtime image 최소화와 compile/execute image 분리

대상 리스크: `SEC-002`, `DOC-001`, `OPS-004`

목표: 제출 코드가 접근하거나 실행할 수 있는 image surface를 줄이고, compile과 execute 위협 모델을 분리한다.

작업:

- 언어별 runtime image에서 shell, package manager, debugger, diagnostics tool, compiler를 제거할 수 있는지 조사한다.
- execute image와 compile image를 분리할 수 있는 언어부터 catalog를 나눈다.
- base image digest pinning 정책을 적용한다.
- runtime catalog 변경은 security-sensitive change로 review template을 만든다.
- compiler도 untrusted parser surface라는 점을 compile runner topology에 반영한다.

검증:

- execute image에서 `/bin/sh`, package manager, compiler binary 실행 probe를 추가한다.
- compile-only image와 execute-only image가 필요한 artifact handoff contract를 지키는지 테스트한다.
- image build CI에서 Trivy 또는 Grype scan, SBOM 생성, digest drift check를 실행한다.

완료 조건:

- 실행 단계 image에는 문제 풀이에 필요한 최소 runtime만 남는다.
- compile 단계와 execute 단계가 동일 privilege boundary를 공유한다는 암묵적 가정이 제거된다.

## Phase 7: API abuse control과 quota plane

대상 리스크: `OPS-003`, `RES-002`, `API-001`, `API-002`

목표: auth 이후에도 per-principal cost와 SSE connection을 제어한다.

작업:

- bearer token, platform identity, gateway principal 중 하나를 normalized principal로 매핑한다.
- compile/execute request rate, active run, pending queue, body size, artifact size, SSE connection 수를 principal별로 제한한다.
- request cost model을 만든다: language, compile 필요 여부, time/memory limit, body size, expected output size를 반영한다.
- gateway layer rate limit과 application layer quota의 책임 경계를 문서화한다.
- quota exceed는 judge failure가 아니라 API rejection으로 일관되게 응답한다.

검증:

- 동일 principal burst, 여러 principal mixed load, long-lived SSE connection cap regression을 추가한다.
- queue full, quota exceeded, auth failure, validation failure가 서로 다른 status/log reason으로 남는지 확인한다.
- remote runner mode에서도 control plane quota가 runner overload 전에 작동해야 한다.

완료 조건:

- 공개 endpoint로 운영해도 인증된 사용자 한 명이 runner pool 전체를 쉽게 점유하지 못한다.
- 운영자가 quota를 token/user/tenant 단위로 조정할 수 있다.

## Phase 8: judging determinism과 runtime tuning config

대상 리스크: `JUDGE-002`, `CONF-001`, `JUDGE-003`

목표: TLE/MLE/WLE 판정과 runtime option을 deployment마다 설명 가능하고 조정 가능하게 만든다.

작업:

- CPU time vs wall time 정책, time multiplier, JIT/GC runtime 보정 기준을 문서화한다.
- JVM, Node, .NET, Erlang, Kotlin, Wasmtime 등 runtime option을 config schema로 이동한다.
- config validation에서 sandbox를 약화하는 옵션을 거부한다.
- output truncation flag와 OLE/WA/RE verdict mapping을 protocol docs와 SSE docs에 맞춘다.
- language별 stress corpus를 만들고 Cloud Run helper mode와 self-hosted cgroup mode 결과 차이를 기록한다.

검증:

- JIT warmup-heavy, GC-heavy, stdout-heavy, stderr-heavy, memory-spike submissions를 regression으로 추가한다.
- invalid runtime option은 startup 또는 request validation에서 fail-closed한다.
- final response와 SSE response가 truncation/verdict reason을 다르게 말하지 않아야 한다.

완료 조건:

- 운영자가 언어별 runtime tuning을 코드 수정 없이 조정할 수 있다.
- 판정 drift가 남더라도 어디서 발생하는지 문서와 테스트로 설명 가능하다.

## Phase 9: SPJ hardening 확대

대상 리스크: `SPJ-001`, `SPJ-002`, `SPJ-003`, `TEST-001`

목표: 이미 적용한 SPJ permission/cwd/limits 개선을 language-specific judge integrity test로 확장한다.

작업:

- Python, Node, Java 계열 SPJ에서 cwd import, module path, classpath, dynamic library lookup hijack fixture를 만든다.
- SPJ 전용 workspace에는 judge-owned files와 read-only input/answer/output만 존재하도록 package contract를 정한다.
- SPJ failure reason을 contestant failure와 구분하는 protocol/log taxonomy를 다듬는다.
- problem package format에서 SPJ limits와 runtime option override의 허용 범위를 명시한다.

검증:

- participant output directory에 악성 module/config/library가 있어도 SPJ가 영향을 받지 않아야 한다.
- SPJ limit exceeded, SPJ runtime error, contestant wrong answer가 구분되어야 한다.
- dropped UID에서 SPJ가 필요한 파일을 읽는 permission regression을 계속 유지한다.

완료 조건:

- SPJ는 participant writable state가 아니라 judge-owned clean context에서만 실행된다.
- SPJ 오류와 contestant verdict가 운영 로그와 API 응답에서 혼동되지 않는다.

## Phase 10: remote runner observability와 stream reliability

대상 리스크: `REMOTE-001`, `REMOTE-002`, `JUDGE-001`, `JUDGE-004`

목표: 이미 추가한 stream bound를 운영 관찰성과 heartbeat/timeout 정책으로 완성한다.

작업:

- remote SSE heartbeat idle timeout과 retry/backoff 정책을 명시한다.
- remote transport timeout, oversized event, malformed event, runner disconnect를 structured error로 분류한다.
- sidecar/image stream cap hit를 metrics/log에 남긴다.
- control plane과 runner의 protocol version mismatch handling을 추가한다.
- stream cap 값은 config화하되 안전한 default와 hard maximum을 둔다.

검증:

- no heartbeat, slow stream, oversized line/event, malformed JSON, disconnect-after-partial-event fixture를 추가한다.
- sidecar/image stream cap hit가 final response diagnostic과 server log에 일관되게 남는지 확인한다.
- config가 hard maximum을 넘으면 startup 또는 validation에서 실패해야 한다.

완료 조건:

- remote runner 장애가 contestant verdict처럼 보이지 않는다.
- stream/resource cap이 운영자가 관찰 가능한 event로 남는다.

## Phase 11: adversarial security regression suite

대상 리스크: `TEST-001`, `ARCH-001`, `SEC-001`, `SEC-002`, `SEC-003`, `RES-001`

목표: static review에서 지적된 주요 공격 시나리오를 반복 실행 가능한 regression suite로 만든다.

작업:

- symlink/hardlink output escape, magiclink, cross-device path, non-regular output probe를 유지/확장한다.
- `/proc` read attempts, `/etc` read attempts, socket attempts, fork bomb, many-small-file creation, mmap spike, `/bin/sh` exec probe를 추가한다.
- cgroup/mount namespace backend가 생긴 뒤에는 Cloud Run helper expected-fail과 self-hosted isolated expected-pass matrix를 분리한다.
- remote SSE huge event, sidecar huge file, image stream flood, SPJ import hijack fixtures를 통합한다.
- test runtime이 너무 길어지지 않도록 short smoke와 privileged/integration suite를 분리한다.

검증:

- unprivileged CI에서 돌아가는 smoke suite와 self-hosted privileged runner에서만 돌아가는 isolation suite를 구분한다.
- 보안 정책 변경은 해당 suite의 snapshot/result diff를 남긴다.
- 새 language runtime 추가 시 최소 adversarial probes가 자동으로 실행된다.

완료 조건:

- 주요 sandbox regression이 문서상 주장에만 의존하지 않는다.
- backend별 known limitation이 테스트 matrix에서 명시적으로 보인다.

## Phase 12: deployment templates와 rollout policy

대상 리스크: `OPS-001`, `OPS-002`, `OPS-003`, `OPS-004`, `ROADMAP-001`

목표: 구현된 보안 모델이 실제 배포 설정에서 drift되지 않게 한다.

작업:

- Cloud Run template은 single-slot, denied egress, no secrets in image/env, bounded work root, low-permission service account를 강제하거나 검증한다.
- `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS`로 required work root가 실제 `tmpfs`인지 선택적으로 startup에서 검증하고 Cloud Run 예시 환경에 고정한다.
- self-hosted Helm/Terraform/systemd template은 private runner ingress, cgroup v2, mount namespace prerequisites, runner pool scaling을 표현한다.
- release pipeline에 govulncheck, image scan, SBOM, image signing, digest pinning check를 연결한다.
- feature flag 또는 config gate로 Cloud Run helper mode와 self-hosted isolated mode를 단계적으로 rollout한다.
- 운영 runbook에 incident signals를 정리한다: queue pressure, cgroup OOM, stream cap hit, SPJ failure, runner isolation preflight failure.

검증:

- deployment template lint 또는 dry-run test를 추가한다.
- unsafe public runner exposure, unlimited queue, missing auth, unsafe concurrency config를 preflight에서 잡는다.
- release artifact가 SBOM/signature/digest metadata를 포함하는지 확인한다.

완료 조건:

- 보안 모델이 README 문장에만 존재하지 않고 배포 템플릿과 preflight check로 강제된다.
- public OJ 운영 전 필요한 최소 guardrail이 template 수준에서 재현 가능하다.

## 최종 목표 상태

- Cloud Run mode는 계속 실용적인 single-slot helper hardening 모델로 남되, no-secrets/denied-egress/private-runner 조건이 강제된다.
- self-hosted mode는 per-run cgroup, private mount namespace, masked proc, bounded workdir, per-run identity를 제공한다.
- syscall, filesystem, process, memory, stream, API quota 리스크가 각각 코드와 regression으로 추적된다.
- compile, execute, SPJ, remote runner가 모두 같은 수준의 threat model 문서와 운영 검증을 갖는다.
