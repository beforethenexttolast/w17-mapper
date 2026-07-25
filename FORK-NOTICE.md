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

A local `pre-push` hook guards points 1 and 3 above. **It is a speed bump, not a
control:** `--no-verify` bypasses it, and hooks are not cloned, so a fresh clone
has no guard. The real gate is the review checklist and owner approval.
