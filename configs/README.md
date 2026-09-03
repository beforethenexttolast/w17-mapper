# W17 saved profiles

`w17-ds4.json` is the committed DualShock 4 profile for the W17 — the config
the race-day bring-up loads headlessly. It exists so the giftee never opens
the node-graph editor: the mapper is a build-time tool, the profile is the
product. It is test-pinned (`pkg/config/w17_profile_test.go`): schema-valid,
cycle-free, zero lint findings, and the exact channel map below — a drive-by
edit that breaks any of that fails the suite.

## Loading

```sh
elrs-joystick-control -config-file-path configs/w17-ds4.json
```

One flag. The profile's own `tx.port` is the port, so it does not have to be
repeated on the command line: after the config is applied, the mapper starts
the RF link on that port itself, at the `-tx-serial-port-baud-rate` default
(921600). This is owner decision **OD-5(a)**, and it is what lets the ground
station launch the mapper with the single flag its argv whitelist carries.

An explicit `-tx-serial-port-name` still wins outright — the hobbyist path is
unchanged, and no second link is started.

The config is applied through the same gRPC `SetConfig` the editor uses, so
it passes the same schema validation, the read-cycle check, the placeholder
refusal below, and the W17 plausibility lint (`(config-lint)` warnings in the
log — the shipped profile must produce none).

Bring-up happens in this exact order, and the order is load-bearing:

1. the file's `{"config": …}` wrapper is unwrapped (the RPC carries the config
   *object*; the server re-wraps it before validating);
2. any unfilled `REPLACE-WITH-*` placeholder is **refused**;
3. only then is the link started — which is why the self-start can never open
   the literal string `REPLACE-WITH-COM-PORT`.

### The two placeholders — and the refusal

**Two values are machine-specific and shipped as placeholders.**

| Placeholder | Where | Replace with |
|---|---|---|
| `REPLACE-WITH-DS4-ID` | every `gamepad.id` | the pad's id from the mapper UI gamepad list (an md5-derived 6-char hash of the SDL GUID+name — device-specific, so it cannot be committed) |
| `REPLACE-WITH-COM-PORT` | `tx.port` | the ELRS TX serial port (e.g. `COM5`) — this is also the port the link is started on |

**A profile with either placeholder still in it is REFUSED, not loaded**
(owner decision OD-9/D3). Both load paths refuse it — the headless
`-config-file-path` bring-up and the editor's Apply — with one sentence:

```
this saved profile has not been matched to this computer yet: it still contains
the placeholder values "REPLACE-WITH-COM-PORT", "REPLACE-WITH-DS4-ID", which
have to be replaced with the real ones for this machine before the car can be
driven -- see configs/README.md
```

Before this, an unfilled profile *loaded*. The state was fail-safe — an
unresolvable pad keeps every channel at its failsafe and arm stays on the 172
disarm rail — but completely silent: the placeholders are legal strings, so the
schema, the cycle check, the lint, the profile tests and the ground station's
pre-launch checks all accepted them, and the only symptom was a car that would
not move.

The committed file must **keep** both placeholders; a test fails if a
bench-filled copy is ever committed.

## Ports the mapper opens

Both listeners bind **loopback only** by default (owner decision OD-8(a)):
`127.0.0.1:10000` for gRPC and `127.0.0.1:3000` for the web UI. Neither is
authenticated, and on race day the laptop hosts the phone's Wi-Fi hotspot, so
"reachable from the network" is not hypothetical. Every legitimate client is
local — the ground station dials `127.0.0.1:10000` — so the default costs
nothing.

- `-bind-host <ip>` — listen somewhere else.
- `-bind-all` — listen on every interface, the way builds before this one did.
  For the hobbyist path only, on a network you trust.
- `-pprof` — mount the Go profiling handlers on the web-UI port. **Off by
  default**: a single CPU-profile request stalls the process that is driving
  the car.

## Packaged release

`.github/workflows/w17-windows-release.yaml` bundles this file alongside the
binary — a giftee-facing zip is the intended distribution shape, not a bare
`.exe`. **(Not yet executed on any runner — no W17 release has ever run this
workflow; the description below is the workflow's design, not an observed
green run.)** On push to `w17-headtrack`, a `w17-v*` tag, or
`workflow_dispatch`, it builds `windows-amd64` with the same builder image, Go
version, and CGO/SDL setup upstream's own Windows job uses, runs
`go test -tags static ./...` (matching the shipped `-tags static` build), and
assembles `w17-mapper-windows-amd64.zip`:

```
w17-mapper-windows-amd64/
  elrs-joystick-control.exe
  configs/
    w17-ds4.json
    README.md          (this file)
  FORK-NOTICE.md
  LICENSE
  LICENSE-GPL          (GPL v3 full text — this fork's elected licence)
  LICENSE-FAIR-SOURCE  (Fair Source 0.9 full text — upstream's other option, not elected)
  W17-README.txt        (generated at package time — build ref, quickstart, known issues)
```

The artifact uploads on every run and, on tag builds, attaches to the GitHub
release.

Cosmetic note: `w17-v*` tags (e.g. `w17-v1.0.0`) do not match
`scripts/cmd/generate-version-file/generate-version-file.go`'s release-tag
regex `^v\d+\.\d+\.\d+` (it anchors at the string start, and the tag starts
with `w17-`, not `v`), so the version embedded in the binary via `go generate`
falls back to its `(devel)` default instead of the tag — this README's own
`Build:` line in `W17-README.txt` is generated independently from
`github.ref_name`/`github.sha` and is unaffected.

**How the ground station consumes it.** Race day does not read this repo's
docs or run the `Loading` command above by hand — it launches the exe
directly with exactly one flag, built by a pure whitelist function:
`mapperArgv` returns `['-config-file-path', <profilePath>]`
(`w17-ground-station/main/raceDayOrchestrator.js:44,50-71`), and
`_mapperStep` calls it with the operator's saved profile path
(`w17-ground-station/main/raceDayOrchestrator.js:268`) before handing the
result to `MapperRunner.start({ binaryPath, argv })`
(`w17-ground-station/main/raceDayOrchestrator.js:279`,
`w17-ground-station/main/mapperRunner.js:99`), which spawns `binaryPath` with
`cwd: path.dirname(binaryPath)` and an environment scrubbed of the entire
`W17_*` namespace (`w17-ground-station/main/mapperRunner.js:110,113`). Both fields are
ground-station **settings the operator sets once**, on the ⚙ RACE DAY row,
under the visible sub-labels **"drive program"** and **"saved profile"**
(`w17-ground-station/renderer/index.html:564-565`) — internally stored as
`mapperPath`/`profilePath` (`w17-ground-station/shared/settings.js:220,226-227`).
Both must be OS-absolute — a relative value is refused before launch
(`w17-ground-station/main/raceDayOrchestrator.js:60-69`). After unzipping this
bundle anywhere on the Windows PC, point:

- **drive program** → `...\w17-mapper-windows-amd64\elrs-joystick-control.exe`
- **saved profile** → `...\w17-mapper-windows-amd64\configs\w17-ds4.json`

**What is proven, and what is still owed.** The bring-up above is covered
end to end by `pkg/client/headless_bringup_test.go`, which drives this exact
committed file through the real client → gRPC → `SetConfig` path against an
in-process server: the profile loads, an unfilled one is refused with the
sentence above, and `StartLink` is called with the profile's own `tx.port`.

What those tests do **not** prove, because no test can: that a radio answered.
`StartLink` is recorded rather than executed there — no serial port is opened —
so **"CRSF frames observed on the wire after one RACE DAY press" remains an
owed bench check** `[bench-TBD]`, together with the reconnect behaviour after a
real unplug/replug. Until that check is done, treat "the link was started" as
"the mapper asked for the right port", not as "the car is hearing it".

## Channel map (firmware truth: control-fw `lib/channels` at `3f4f9b7`)

Every channel uses the CRSF anchors `crsf_min 172 / crsf_max 1811`. The
firmware treats values outside its 100/1900 plausibility band as ABSENT, so
the upstream schema defaults (0/1984) can never arm the car — that is audit
defect 3, and the lint now flags it.

| CRSF ch | Control | Kind | DS4 binding (SDL HIDAPI layout) | Failsafe |
|---|---|---|---|---|
| 1 | steering | analog | left stick X (axis 0, deadzone 2000) | 992 center |
| 3 | throttle | analog | R2 forward, L2 brake/reverse (axes 5/4, each rescaled 0..32767, subtracted) | 992 center |
| 5 | arm | 2-pos switch | TRIANGLE (button 3) toggle — see arm chain below | **172 OFF** |
| 6 | DRS | 2-pos switch | hold SQUARE (button 2) | **172 closed** |
| 7 | gear up | momentary | R1 (button 10) | **172** |
| 8 | gear down | momentary | L1 (button 9) | **172** |
| 9 | gimbal pan | analog | right stick X (axis 2) — stick-driven, NOT head tracking | 992 center |
| 10 | gimbal tilt | analog | right stick Y (axis 3) — stick-driven, NOT head tracking | 992 center |
| 11 | ERS boost | 2-pos switch | **pinned OFF** (number node at the rail) | **172** |
| 12 | ERS overtake | 2-pos switch | **pinned OFF** | **172** |
| 13 | drive mode | 3-pos | **pinned LOW = TRAINING** (gentle default; RACE=mid, ERS=high — rebind later) | 172 (TRAINING) |

Failsafe rationale (audit defect 1): the firmware decodes switches with ±250
hysteresis around center, so a switch channel that fails to 992 **holds its
previous state** — an armed car would stay armed through a pad dropout. Every
decodeSwitch channel therefore fails to the 172 OFF rail; the load-time lint
asserts exactly that.

## Reserved inputs — do not bind

**SHARE (button 4), OPTIONS (button 6) and the D-pad (hat 0, DOWN especially)
are deliberately unbound.** They are reserved for the recorded head-tracking
affordances (Alternative C: D-pad-DOWN deadman, SHARE/OPTIONS
recenter/enable) so that no rebinding is needed when that gated milestone is
reached. The reservation is by **physical button** — under the DirectInput
fallback those same two buttons enumerate as 8/9 (see the layout section).
Profile tests fail if either acquires a binding, and pin the bound set to
exactly SQUARE 2 / TRIANGLE 3 / L1 9 / R1 10. Also unbound: CROSS (0),
CIRCLE (1), PS (5), L3/R3 (7/8), touchpad.

## The arm chain, and why it is not just a toggle

`ch5 = and(seq-toggle{reset_on_nan}, liveness-probe)`:

- the `seq` toggles 0/32767 on a deliberate TRIANGLE press-and-release
  (50–1000 ms) and **boots disarmed**;
- a naked `seq` would **hold its state when the pad dies** (its conditions go
  nan and it keeps returning the current value — `input_seq.go`), quietly
  re-opening the latch defect;
- `reset_on_nan` closes the reconnect half of that hazard: **any dropout
  returns the toggle itself to DISARMED**, and a press that started before or
  spanned the dropout is discarded — re-arming always takes a fresh, full
  release-then-press after the pad is back. A controller auto-reconnect can
  therefore never silently re-arm the car (the firmware ArmGate has no
  reconnect policy of its own; the mapper must not hand it an armed channel
  uninvited);
- the liveness probe (left stick X through a `linear` pinned to constant 1)
  stays as defense in depth: pad dead → probe nan → the whole `and` goes nan →
  the channel falls to its 172 failsafe for the duration of the outage. The
  probe sits deliberately on the `and`'s RIGHT side — upstream `and` swallows
  a nan LEFT operand and only an all-nan RIGHT side fails the node
  (`input_and.go`), so a device-fed probe on the LEFT would lose its failure
  signal. Do not flip that shape in a future profile without revisiting.

## SDL layout — which one, and the bench check owed

The profile binds the **SDL HIDAPI PS4 driver layout**, the default under the
bundled SDL2: joystick buttons come out in GameController order — CROSS 0,
CIRCLE 1, **SQUARE 2, TRIANGLE 3**, SHARE 4, PS 5, OPTIONS 6, L3 7, R3 8,
**L1 9, R1 10** — and axes are LX 0, LY 1, RX 2, RY 3, L2 4, R2 5.

The legacy **DirectInput fallback moves BOTH axes and buttons** (an earlier
revision of this file claimed buttons were stable across the two paths; that
was false): buttons revert to the raw-HID order (SQUARE 0, CROSS 1, CIRCLE 2,
TRIANGLE 3, L1 4, R1 5, L2 6, R2 7, SHARE 8, OPTIONS 9, L3 10, R3 11, PS 12,
touchpad 13) and axes to L2 3, R2 4, RY 5.

**Pre-drive bench check (owed, same class as the wheel check):** open the
mapper UI with the pad connected and verify BOTH halves —

1. axes: wiggle right stick Y and squeeze each trigger; expect RY on axis 3,
   L2 on 4, R2 on 5;
2. buttons: press SQUARE, TRIANGLE, L1, R1 one at a time; expect 2, 3, 9, 10.

If the pad instead shows the DirectInput numbers, remap in one pass: axes
tilt→5, L2→3, R2→4; buttons DRS→0, arm stays 3, gear down→4, gear up→5; and
note the reserved pair SHARE/OPTIONS then lives at 8/9 (the reservation is by
physical button, not by index).
