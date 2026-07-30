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
  FIRST_ACTIVE review checklist (R1–R14) passes.

### Push-review rule (the actual control)

**No push to a public remote may add head-intent shaping, arbitration, or any
output path until the FIRST_ACTIVE review checklist R1–R14 passes and the owner
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
`FIRST_ACTIVE` identifier in Go/proto source, or an active head-intent enum
value. Prose that merely documents the boundary (this file) does not trip it.

**Honest limits:** `--no-verify` bypasses it; it scans the pushed tip tree, not
every commit in the range; and it does nothing in a clone where
`core.hooksPath` was never set. It is a speed bump against accident. The
push-review rule above, the R1–R14 checklist, and owner approval are the gate.
