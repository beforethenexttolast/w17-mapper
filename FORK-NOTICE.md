# Fork notice — `w17-mapper`

This repository is a **modified fork**. It is not the upstream project.

## Provenance

| | |
|---|---|
| Upstream project | **ELRS Joystick Control** — https://github.com/kaack/elrs-joystick-control |
| Upstream copyright | Copyright (C) 2023 OneEyeFPV |
| Fork point | commit **`2b8031a`** (verified an ancestor of this branch) |
| Fork branch | `w17-headtrack` |
| Fork purpose | head-intent ingest for the W17 1/10-scale RC project |

Upstream's licence files are preserved **unmodified**: `LICENSE`, `LICENSE-GPL`,
`LICENSE-FAIR-SOURCE`.

## Licence election

Upstream is **dual-licensed** — GNU GPL version 3 (or any later version) **or**
Fair Source License 0.9, recipient's choice.

**This fork elects the GPL: `GPL-3.0-or-later`.** (Owner decision, recorded
2026-07-15 in `w17-control-fw/project-review/head_tracking_unlock_plan.md`
§2.3.12.9, R11 PASS.) The Fair Source option carries a 1-user limitation and was
not taken. Upstream's own dual-licence offer is untouched and continues to apply
to upstream's work — this election governs only this derivative.

Because GitHub's licence detector reads the ambiguous dual-licence pointer in
`LICENSE`, the repository badge may show "Other". The election above is the
authoritative statement for this fork.

## Changes from upstream (GPL §5(a) modification notice)

All changes are dated and attributable in git history. Summary:

| Commit | Date | Change |
|---|---|---|
| `59d1739` | 2026-07-15 | **LOG-ONLY** head-intent ingest: new self-contained `pkg/headintent` (UDP 5602 receiver, validator, state machine), a read-only `WatchHeadIntentDiagnostics` gRPC stream, and a pinned/drift-checked proto-codegen script (`pkg/proto/generate.sh`) |
| `f0a18f3` | 2026-07-25 | `go.bug.st/serial` v1.5.0 → v1.6.0, to clear a go1.26 cgo build failure in third-party `enumerator` |
| `2dc7c5a` | 2026-07-30 | **Failsafe fix** in the pre-existing stick path: a channel whose input evaluates to `nan` is driven to a defined neutral instead of silently holding the previous tick's value. Adds a per-channel `failsafe` config field (`ChannelT.Failsafe`, schema + default `util.CRSFCenterValue` = 992), `devices.InputGamepad.Attached()` so a detached gamepad stops resolving, and centered rather than zeroed initial transmitter values |
| `630ea96` | 2026-07-30 | **Failsafe fix** on the no-config path: a port with no config now transmits no channel frame at all, instead of the all-zeros `EvalNoData` array, so the receiver's link-loss failsafe can fire rather than being masked by well-formed frames. Removes the unused `Controller.EvalCenter` (all 992 sits inside a receiver's switch hysteresis band and would hold an arm switch latched) and its commented-out send line; `EvalNoData` is retained as a display-only value. A failed model-id write now tears the send loop down, preserving serial-disconnect detection that previously depended on the channel write |
| `e452d55` | 2026-08-03 | **Failsafe fix**, two paths the previous two did not reach. (1) `OutputTransmitter.Eval` resolves a channel's neutral from the `channel` node that OWNS the number rather than from the top-level holder, and neutralizes every channel under a holder whose result is unusable — a wrapper node at the top level (14 of the 27 node types report `ch = -1` once their input fails) previously left the slot holding its last value, and the 4 `EvalOperation` types silently replaced a configured failsafe with center. (2) `SendLoop` suppresses channel frames for a bounded-below window after a config is applied, so the receiver's link-loss failsafe fires across the swap instead of the mapper transmitting the re-seeded center value on channels the new config no longer maps. `EvalLoop` also now evaluates the synthetic transmitters before publishing them |
| `c60843e` | 2026-08-04 | **Failsafe fix**, completing the previous row. (1) The neutral is now resolved per OWNER rather than from the holder's result alone: `channelOwners` walks the subtree before evaluation, arming each `channel` node, and any owner the evaluation did not resolve is driven to its own failsafe even when the holder reports healthy. `EvalOperation`, the `and`/`or` right-operand loops and `EvalRelational` all ignore a `nan` operand, so a subtree fed by two devices kept transmitting a dead channel on a link that looked healthy. (2) `channelOwnerMaxDepth` 32 → 256, and a truncated walk is now fail-safe rather than silently holding: the new `OutputTransmitter.Unresolved` flag (published per port alongside the channel array) makes `SendLoop` suppress that port's frames, since an incomplete owner set means the entry's channels are unknown. Corrects the depth bound's comment, which described it as a `read`-cycle backstop it cannot be — `InputRead._Eval` recurses through `Config.IOMap` unguarded and overflows the stack first; that upstream recursion is unchanged here and tracked separately |
| `b28d04b` | 2026-08-16 | **Crash fix** in the pre-existing `read` path: `InputRead._Eval` recursed through `Config.IOMap` unguarded, so a schema-valid `read` cycle overflowed the stack and killed the process (2026-08-16 audit, defect 4). A re-entrancy guard on the node now turns a cycle into `nan` -- the existing failsafe machinery then neutralizes or suppresses -- and the new `Config.CheckReadCycles` makes the server's `SetConfig` refuse a cyclic config before applying it, with the loop spelled out. `SetConfig` also no longer applies a config whose JSON decode failed. This closes the upstream recursion the `c60843e` row above records as "tracked separately" |
| `42db52c` | 2026-08-16 | **Failsafe fix**, removing the last subscriber dependency (2026-08-16 audit, defect 2). Transmitter re-evaluation was entirely event-driven: `AlertDeviceChan` is a lossy non-blocking send with competing receivers, and the 25 ms re-evaluation tickers all live inside streaming RPC handlers — so with no gRPC subscriber, one dropped removal alert left stale values transmitting at full rate with every failsafe in place but never run. `EvalLoop` now re-evaluates the synthetic transmitters on its own 25 ms heartbeat, unconditionally; an input death reaches the published arrays within one interval. `StartEvalLoop` also creates the loop's event channels before spawning it, so late subscribers stop reading those fields unsynchronized |
| `5191667` | 2026-08-16 | **Hat direction decode** (2026-08-16 audit, defect 13). Hats were direction-blind: the only read mapped the SDL hat BITMASK through a [-1, 1] range, so every pressed position clamped to the same value. The `hat` node now takes an optional `direction` (`up`/`down`/`left`/`right`, schema enum + load-time validation) decoding that one direction as a momentary button, diagonals counting for both components; the pure decode is test-pinned across the full SDL truth table. Absent `direction` keeps the legacy scalar read unchanged |
| `6ecad6e` | 2026-08-16 | **W17 profile + plausibility lint** (2026-08-16 audit, defects 1 and 3). Ships `configs/w17-ds4.json`, the committed DualShock 4 profile the race-day bring-up loads via `-config-file-path`: every channel on the 172/1811 CRSF anchors (the schema's 0/1984 defaults sit outside the firmware's 100/1900 plausibility band and can never arm), the 172 OFF-rail failsafe on all six firmware decodeSwitch channels (a 992 failsafe latches in the ±250 hysteresis), the arm toggle liveness-gated so a pad dropout disarms, and SHARE/OPTIONS/D-pad left unbound (reserved, head-tracking Alternative C). Adds `config.LintConfig` — load-time warnings through `SetConfig` for out-of-band endpoints/failsafes and non-rail switch failsafes — with the committed profile test-pinned to zero findings |
| `141c44a` | 2026-08-17 | **Silent-re-arm fix** (review blocker F2). A `seq` holds its state through an all-nan episode, so an arm toggle survived a gamepad dropout and a controller auto-reconnect re-armed the car with zero user input. `SeqT` gains opt-in `reset_on_nan` (schema + default false, upstream behaviour unchanged when unset): an all-nan episode returns the sequence to `output_values[0]` and demands a fresh release-then-press before the next activation, so a press spanning the dropout cannot toggle either. Also documents, at the code site, upstream `and`'s swallowed nan LEFT operand (review obs 1): a device-fed node on an `and`'s left loses its failure signal — the W17 arm gate deliberately keeps its device-fed probe on the RIGHT, whose all-nan is what fails the node |
| `4e27b0e` | 2026-08-17 | **Profile correction** (review blocker F1). `configs/w17-ds4.json` had mixed SDL HIDAPI axis numbering with raw-HID button numbering — a pairing no Windows driver produces. Buttons now follow the HIDAPI GameController order (DRS SQUARE→2, gear up R1→10, gear down L1→9; TRIANGLE stays 3), the reserved head-tracking pair is SHARE=4/OPTIONS=6, the bound set {2,3,9,10} is test-pinned, the arm seq sets `reset_on_nan: true`, and the README documents both layouts truthfully plus a press-verify bench check for all four bound buttons and the three ambiguous axes |
| `b263057` | 2026-09-03 | **CI addition**: `.github/workflows/w17-windows-release.yaml`, a Windows release-packaging job for this fork. Builds with the same `oneeyefpv/windows-amd64-builder` image, Go version, and CGO/SDL setup upstream's own Windows job uses (`-tags static`, no other build tags), runs `go test ./...` before packaging, and — unlike any upstream workflow — assembles the exe with `configs/w17-ds4.json`, `configs/README.md`, this file, `LICENSE`, and a generated `W17-README.txt` into `w17-mapper-windows-amd64.zip` for the ground station to consume. No source/behaviour change to the mapper itself; the generated README documents, without fixing, the MAP-1 (`-config-file-path` double-wrap panic) and MAP-2 (race day never starts the RF link) review findings. **Independent-review fix pass** (`680e077`, `a03f604`, both 2026-09-03): the bundle now also carries `LICENSE-GPL` and `LICENSE-FAIR-SOURCE` (GPL §4 needs the full licence text alongside the program, and the pointer file alone isn't it); the generated README gained a GPL §6 corresponding-source pointer to this fork's own remote + build SHA, an honest MAP-8 disclosure (unauthenticated gRPC :10000 + grpc-web :3000 on all interfaces, no `-disable-web-ui` mitigation reachable from race day) alongside the existing head-intent boundary statement, and the true ground-station field labels ("drive program" / "saved profile") in place of invented ones; the CI job itself is pinned to `runs-on: windows-2022` (matching the builder image's `servercore:ltsc2022` base), tests with `-tags static` to match the shipped build, hardens the first-tag `gh release view` probe, and gained a `concurrency` group. `configs/README.md` picked up the same label fix, marked the workflow "not yet executed on any runner", documented the mapper's web-UI RF Link panel as today's only way to actually start the link, and noted that `w17-v*` tags don't match `generate-version-file.go`'s tag regex. Still no source/behaviour change to the mapper itself; MAP-1, MAP-2 and MAP-8 remain open for a separate fix wave |
| `b4dd7a4` | 2026-09-03 | **Build-graph change, no behaviour change.** `pkg/server` takes the web-UI lifecycle as a two-method `HTTPController` interface instead of the concrete `*pkg/http.Controller`, and (in `50f06c8`) `pkg/http` takes the built bundle as an `http.FileSystem` argument instead of importing `webapp` itself. `pkg/http` embeds `webapp/dist`, which does not exist until `go generate ./...` has run npm and webpack, so every package importing either one was unbuildable on a clean checkout — `pkg/server`'s own tests had therefore never run outside a builder image, and this fork's CI has never fired, and the headless bring-up path had no end-to-end test at all, which is why `MAP-1` below survived a passing profile suite. (Measured at the base commit `21834fe`: with a stub `webapp/dist` in place, `go test ./...` runs 180 tests; without one, `go test ./pkg/...` runs 171 and `pkg/http`/`pkg/server` are `[setup failed]`.) After both changes NO package under `pkg/` depends on the web bundle; only `cmd` does — so `go test ./pkg/...` is the command that runs on a clean checkout, and `go test ./...` still needs `go generate ./...` first for the embed. Same RPCs, same wiring, same binary; the two HTTP RPCs additionally report `Unavailable` rather than panicking when no web UI was supplied |
| `6353759` | 2026-09-03 | **Bring-up fix (review finding MAP-1)**, the defect that made the documented `-config-file-path` invocation impossible on any machine. `SetConfigReq.Config` carries the config OBJECT; the server re-marshals the whole request, which puts it back under a `"config"` key before validating, and a saved profile already carries that wrapper — so the server saw `{"config": {"config": {…}}}`, `input_output_map` was two levels down, validation failed and the client PANICKED. `client.Init` now unwraps a top-level `"config"` object before encoding, and passes a bare `{"input_output_map": …}` document through unchanged. `Init` also returns an error instead of panicking (every failure here is operator-facing; `main` prints one line and exits non-zero after its normal shutdown) and no longer echoes the whole profile to stdout, which used to flush the ground station's bounded diagnostics ring on every launch. New `pkg/client/headless_bringup_test.go` drives the committed profile through the real client → gRPC → `SetConfig` path — the first test of any kind on that path |
| `16e6fdf` | 2026-09-03 | **Load-time refusal (review finding MAP-5, owner decision OD-9/D3)**: a profile still carrying a `REPLACE-WITH-*` placeholder is refused rather than loaded. The state was fail-safe but SILENT — placeholders are legal strings, so the schema, the read-cycle check, the lint, the profile tests and the ground station's pre-launch checks all accepted them, and an operator's only symptom was a car that would not move. New `config.UnfilledPlaceholders` scans the decoded document and `config.PlaceholderRefusal` renders one plain sentence; the server refuses in `SetConfig` (authoritative, and covers the editor's Apply) and the client refuses before the RPC (so the sentence is read plainly, and so the self-start below can never see the literal placeholder). A test pins that the SHIPPED profile still contains both, so a bench-filled copy cannot be committed. **Placement deviation, recorded rather than re-opened:** the review's fix text asked for the check in `LintConfig` *plus* a hard refusal. `pkg/config/lint.go` is untouched, so `(config-lint)` output still says nothing about placeholders — and cannot, because the refusal runs first and lint never sees an unfilled profile. The refusal is strictly stronger than a warning and is what owner decision OD-9/D3 asked for, so the lint half was dropped deliberately |
| `16dccc0` | 2026-09-03 | **Race-day bring-up (review finding MAP-2, owner decision OD-5(a))**: after a successful `SetConfig` with no explicit `-tx-serial-port-name`, the mapper starts the RF link on the port the loaded profile itself names. Nothing did this before — `StartLink` ran only when that flag was passed, and the ground station's argv whitelist carries exactly one flag — so a launch produced a process that had a config and transmitted nothing while the card said "running", and the only way to start transmitting was this fork's own web UI, the hobbyist step the product removes. `tx.port` is the only port that CAN work, since the send loop resolves channels by matching it. A config with no transmitter, or with several, prints a line and starts none rather than guessing; an explicit flag still wins outright |
| `23e0b9f` | 2026-09-03 | **Config-adoption fix (review finding MAP-4).** `SetConfig` set the field, fired a droppable non-blocking alert on an unbuffered channel and returned success; a dropped alert left the new config visible to `GetConfig` and the editor while the send loop went on transmitting the PREVIOUS config's arrays permanently — the eval loop's 25 ms heartbeat re-evaluates the holders it already has, so it repairs a dropped DEVICE alert but never a dropped CONFIG alert. `ConfigEventChan` is now buffered, delivery is a blocking send with escapes, and the eval loop closes an adoption barrier after swapping the published maps in, so `SetConfig` returns once the send loop can see the new config. Also removes a real data race the fix would otherwise expose: the eval loop read `c.Config` from inside its own goroutine, and now receives it from the caller's. **Superseded in part by `1094065` below:** the barrier this commit added was ONE channel shared by every applier, which holds under a single applier and not under two — see that row |
| `8b105c1` | 2026-09-03 | **Link-lifecycle and keepalive fix (review findings MAP-3 and MAP-10)**, landed together because each masked the other. MAP-3: both of the recv loop's sends to the send loop were bare sends on an unbuffered channel, so when the send loop had exited — which it does on any serial write error, i.e. exactly when the transmitter is unplugged — the recv goroutine parked forever, `StopRecvLoop`'s `Wait()` never returned, and with it the supervisor's reconnect iteration, `StopSupervisor` and the `StopLink` RPC behind the ground station's STOP button. Both sends are now tomb-armed and the supervisor gives the channel a small buffer. MAP-10: elapsed time was divided by `time.Millisecond` before comparison against a nanosecond `Duration`, so the model-id keepalive's 2 ms threshold at 921600 baud became roughly 33 minutes, and `lastRecvTelemTime` was assigned once at loop start and never refreshed. Both corrected. `recvLoop` now takes its telemetry source as an interface so these rules can be tested without opening a serial port |
| `50f06c8` | 2026-09-03 | **Shutdown fix (review finding MAP-11)**: the web-UI shutdown goroutine called `echo.Shutdown(nil)`, and `net/http`'s `Server.Shutdown` selects on `ctx.Done()` — a nil-interface dereference — so any shutdown with a non-idle connection open panicked inside a tomb goroutine, where a panic is a PROCESS fault. The reachable case is the Ctrl-C path, which turned a clean exit into a goroutine dump in the ground station's diagnostics ring. Now a real context with a 5 s deadline, a forced `Close()` if graceful shutdown does not finish, and a `recover` so an HTTP fault can never be a process fault. `pkg/http` also stops importing the embedded bundle (see `b4dd7a4`), which is what makes any of this testable |
| `5d4e12d` | 2026-09-03 | **Bind policy (review finding MAP-8, owner decision OD-8(a))**: both listeners bind loopback by default. Upstream bound the wildcard on gRPC `:10000` WITH server reflection and on grpc-web `:3000` with `Access-Control-Allow-Origin *`, both unauthenticated and without an interceptor, exposing `SetConfig`/`StartLink`/`StopLink`/`SetCRSFDeviceField` — on the laptop race day now makes the host of the phone's Wi-Fi hotspot. New `-bind-host` (default `127.0.0.1`, applied via `net.JoinHostPort` at both listeners), `-bind-all` to restore every interface for the hobbyist path, and `-pprof` gating the runtime profiling handlers OFF (a single CPU-profile request stalls the process driving the car); the unused `net/http/pprof` blank import is dropped. `client.Init` dials the host the process actually bound rather than the name `localhost`, and both startup lines now print the address really bound instead of claiming `[::]` |
| `787e5a8` | 2026-09-03 | **Documentation truth** for the four behaviour changes above. `configs/README.md` documents the one-flag load, the strict bring-up order (unwrap → placeholder refusal → self-start), the refusal sentence, and a new "Ports the mapper opens" section; the release workflow's generated `W17-README.txt` loses its "known issues" block naming MAP-1/MAP-2 as open and its "no mitigation to hand the giftee today" MAP-8 paragraph. Both replace those with an explicit split between what the automated tests prove and what is still owed on a bench: CRSF frames observed on the wire after one RACE DAY press, and reconnect after a real unplug/replug, remain owed. No source change |
| `b071a30` | 2026-09-03 | **Record only**: the nine rows above, added as this fork's GPL §5(a) modification notice for the bring-up fix pass. No source change. (Its own row is this one, added by the fix pass below — the same catch-up convention the `b263057` row uses for `680e077`/`a03f604`.) |
| `b6e641c` | 2026-09-04 | **Bring-up warning (independent-review finding B1)**: an explicit `-tx-serial-port-name` that disagrees with the loaded profile's own `tx.port` now prints one line saying nothing will be transmitted. The send loop resolves a port's channel array by matching the LINK's port name against the map keyed by `tx.port`, and this fork's deliberate answer to "no config resolves" is to write no channel frame at all — so a mismatched flag produces a running process, an open link, a "running" card and a car that cannot move, with nothing naming the reason. `configs/README.md` also restores the port-equality caveat that the previous documentation pass had deleted, in both places it stood |
| `64be585` | 2026-09-04 | **Data race (independent-review finding N2)**: `config.Controller.Config` was written by `SetConfig` and read bare by the gRPC `GetConfig` handler and by `GetEvalStates`, from other goroutines. A `sync.RWMutex` now guards it, with a `GetConfig()` accessor the three in-tree readers use. Scope stated at the code: what is guarded is the POINTER — the node graph it leads to is evaluated in place by the eval loop, and anything walking a live config's nodes still shares that pre-existing upstream hazard |
| `1094065` | 2026-09-04 | **Adoption barrier, per caller (independent-review finding B2)**, replacing the shared barrier of `23e0b9f`. With one channel shared by every applier, caller B could take barrier ch_n, A's adoption could close ch_n, and B — delivering afterwards — would wait on an already-closed channel and return SUCCESS for a config that had not been adopted: MAP-4's own failure mode surviving the fix for it. `ConfigEventChan` now carries a `ConfigEvent{Config, Done}`, `SetConfig` makes and waits on its own `Done`, and the eval loop closes the `Done` that arrived with the config it has just published, in both the swap and the clear branch. `adoptionMu`/`configAdopted` are gone. A regression test runs two appliers for 200 rounds each and fails against the previous implementation |
| `8bd08f4` | 2026-09-04 | **Shutdown error (independent-review finding N3)**: `shutdownEcho` shadowed its named return with `if err := server.Shutdown(ctx)` and ended in a bare `return nil`, so a graceful shutdown that ran its 5 s deadline out reported to nobody — a regression against the base, where that error propagated through the tomb to `Stop()`. It is returned again (joined with a failing forced `Close()`); a recovered PANIC still yields nil, deliberately |
| `32a89b5` | 2026-09-04 | **Typed-nil guard (independent-review finding N4)**: `s.HTTPCtl == nil` is FALSE for an interface holding a typed nil pointer, so the two HTTP RPCs' `Unavailable` guard would be skipped and `Start()`/`Stop()` called on a nil receiver. Not reachable from `main`, which always constructs a real controller; the shape exists because `b4dd7a4` made `HTTPCtl` an interface. One reflect check answers the real question |
| `2cbf6cc` | 2026-09-04 | **Message correction (independent-review finding N5)**: `TransmitterPorts` filters an empty `tx.port` out, so a profile that DOES declare a transmitter with a blank port was reported as "declares no transmitter", sending the operator to look for a missing node instead of an empty field. New `config.TransmitterNodeCount` tells the two apart; neither is an error |
| `cf96c9b` | 2026-09-04 | **Diagnostics rate limit (independent-review finding N9)**: `8b105c1` made the model-id keepalive live, and with it a print that had effectively never run. The reachable burst is a port whose READS error while its WRITES succeed — up to ~2000 loop iterations a second, three stdout lines each, into the ground station's 200-line diagnostics ring. Both recv-loop prints are now limited to one per second each, carrying the suppressed count; the limit governs printing only, not the error counters. The burst rate is arithmetic, not a measurement: `[bench-TBD]` |
| `88d9073` | 2026-09-04 | **Comment only (independent-review finding N10)**: why `TestBindAllIsStillReachable` opens a real wildcard listener in a suite about not binding the wildcard, and what keeps it harmless. No behaviour change |
| `ff25f6f` | 2026-09-04 | **Documentation truth (independent-review findings N1 and B3)**. N1: "reachable from this PC and nowhere else" was true of TCP and misleading as a security statement — the grpc-web port still sets `Access-Control-Allow-Origin: *` unconditionally, so any web page open in a browser ON THAT PC is still an unauthenticated client of `SetConfig`/`StartLink`/`StopLink`/`SetCRSFDeviceField`. Both `configs/README.md` and the release workflow's generated `W17-README.txt` now say what loopback closed and what it did not. B3: the acceptance command is `go test ./pkg/...`, not `go test ./...`, which exits non-zero on a clean checkout until `go generate ./...` has built the web bundle. At this pass's tip `go test ./pkg/...` and `go test -race ./pkg/...` both run 240 tests, 0 failures (180 at the base with a stub bundle); `go vet ./cmd/...` reports only the pre-existing unbuffered `signal.Notify` finding, which is left for a separate branch. No source change in this commit or the row-adding commit that follows it |


## Safety boundary — read before pushing

This fork is part of a project where **iPhone/head-tracking-derived camera
control is deliberately gated behind an unfinished safety review**
("FIRST_ACTIVE", currently **NO-GO / BLOCKED**).

What that means for this code:

- UDP 5602 head-intent ingest is **LOG-ONLY**. It reaches no control output.
- The diagnostics stream is **read-only**.
- `pkg/proto/server.proto` must still end at
  `HEAD_INTENT_STATE_ACTIVE_LOG_ONLY = 8` — there is **no** active enum value.
- `crsf.PackChannels` output is proven **byte-identical** with ingest off vs on
  (12 frames / 312 bytes, across valid / stale / invalid traffic and with
  diagnostics subscribers connected, slow, and disconnected).
- No shaping or arbitration code exists here. None may be published before the
  FIRST_ACTIVE review checklist (R1–R16) passes.

### Push-review rule (the actual control)

**No push to a public remote may add head-intent shaping, arbitration, or any
output path until the FIRST_ACTIVE review checklist R1–R16 passes and the owner
records approval.** That rule is the control; the hook below only catches
accidents.

Before any push touching `pkg/headintent`, `pkg/proto` or `pkg/server`, confirm
all four still hold:

1. `pkg/proto/server.proto` ends at `HEAD_INTENT_STATE_ACTIVE_LOG_ONLY = 8`.
2. No `FIRST_ACTIVE` identifier or `w17_first_active` build tag in source.
3. `go test ./pkg/headintent/` green, including the `crsf.PackChannels`
   byte-identity proof (flag-off vs flag-on).
4. `go list -deps ./pkg/headintent/` still reaches no control/output package
   (no config / link / crossfire / serial / devices edge).

### Hook (accident guard)

`.githooks/pre-push` is **tracked in this repository**, so it survives a clone.
Git does not enable repository-supplied hooks automatically — enable it once per
clone:

```sh
git config core.hooksPath .githooks
```

It refuses a push whose tip tree contains a `w17_first_active` build tag, a
`FIRST_ACTIVE` identifier in Go/proto source, an active head-intent enum value,
or — added 2026-08-04 — any `first_active` / `firstActive`-style identifier in
code, matched case-insensitively. Prose that merely documents the boundary (this
file) does not trip it: the hook greps **code globs only**, and `.md` is not
among them.

**What it catches, and what it does not** (verified 2026-08-04, each injection
committed on a throwaway branch and never pushed):

| Case | Result |
|---|---|
| clean HEAD | allowed ✅ |
| `//go:build w17_first_active` | refused |
| `const FIRST_ACTIVE = false` (Go) | refused |
| `HEAD_INTENT_STATE_ACTIVE = 9` (proto) | refused |
| `const firstActive = false` (Go) | refused — **was allowed before 2026-08-04** |
| `const enableShaping = false` (Go) | **allowed** — documented limit, see below |

**Honest limits:** the hook matches **names, not the class of compile-time
gates** — a const under any other name (`enableShaping`, `gateOpen`) passes
clean, and no grep can close that. This is why
`head_tracking_unlock_plan.md` §2.3.11.4 is resolved to the Go build tag
**exclusively**, with the const alternative deleted rather than kept as a
fallback, and why the tag must be lowercase `w17_first_active` exactly — that
literal is what the hook greps. Additionally: `--no-verify` bypasses it; it
scans the pushed tip tree, not every commit in the range; and it does nothing in
a clone where `core.hooksPath` was never set. It is a speed bump against
accident. The push-review rule above, the R1–R16 checklist, and owner approval
are the gate.
