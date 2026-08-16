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
