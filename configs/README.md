# W17 saved profiles

`w17-ds4.json` is the committed DualShock 4 profile for the W17 — the config
the race-day bring-up loads headlessly. It exists so the giftee never opens
the node-graph editor: the mapper is a build-time tool, the profile is the
product. It is test-pinned (`pkg/config/w17_profile_test.go`): schema-valid,
cycle-free, zero lint findings, and the exact channel map below — a drive-by
edit that breaks any of that fails the suite.

## Loading

```sh
elrs-joystick-control \
  -config-file-path configs/w17-ds4.json \
  -tx-serial-port-name COM5 -tx-serial-port-baud-rate 921600
```

The config is applied through the same gRPC `SetConfig` the editor uses, so
it passes the same schema validation, the read-cycle check, and the W17
plausibility lint (`(config-lint)` warnings in the log — the shipped profile
must produce none).

**Two values are machine-specific and shipped as placeholders. The profile is
fail-safe until both are set** (an unresolvable pad keeps every channel at its
failsafe: arm stays at the 172 disarm rail).

| Placeholder | Where | Replace with |
|---|---|---|
| `REPLACE-WITH-DS4-ID` | every `gamepad.id` | the pad's id from the mapper UI gamepad list (an md5-derived 6-char hash of the SDL GUID+name — device-specific, so it cannot be committed) |
| `REPLACE-WITH-COM-PORT` | `tx.port` | the ELRS TX serial port (e.g. `COM5`); must equal `-tx-serial-port-name` or the send loop resolves no channels |

## Channel map (firmware truth: control-fw `lib/channels` at `3f4f9b7`)

Every channel uses the CRSF anchors `crsf_min 172 / crsf_max 1811`. The
firmware treats values outside its 100/1900 plausibility band as ABSENT, so
the upstream schema defaults (0/1984) can never arm the car — that is audit
defect 3, and the lint now flags it.

| CRSF ch | Control | Kind | DS4 binding | Failsafe |
|---|---|---|---|---|
| 1 | steering | analog | left stick X (axis 0, deadzone 2000) | 992 center |
| 3 | throttle | analog | R2 forward, L2 brake/reverse (axes 5/4, each rescaled 0..32767, subtracted) | 992 center |
| 5 | arm | 2-pos switch | TRIANGLE (button 3) toggle — see arm chain below | **172 OFF** |
| 6 | DRS | 2-pos switch | hold SQUARE (button 0) | **172 closed** |
| 7 | gear up | momentary | R1 (button 5) | **172** |
| 8 | gear down | momentary | L1 (button 4) | **172** |
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

**SHARE (button 8), OPTIONS (button 9) and the D-pad (hat 0, DOWN especially)
are deliberately unbound.** They are reserved for the recorded head-tracking
affordances (Alternative C: D-pad-DOWN deadman, SHARE/OPTIONS
recenter/enable) so that no rebinding is needed when that gated milestone is
reached. A profile test fails if any of them acquires a binding. Also
unbound: CROSS, CIRCLE, L2/R2 digital clicks, L3/R3, PS, touchpad.

## The arm chain, and why it is not just a toggle

`ch5 = and(seq-toggle, liveness-probe)`:

- the `seq` toggles 0/32767 on a deliberate TRIANGLE press-and-release
  (50–1000 ms) and **boots disarmed**;
- a naked `seq` would **hold its state when the pad dies** (its conditions go
  nan and it keeps returning the current value — `input_seq.go`), quietly
  re-opening the latch defect;
- so the toggle is AND-ed with a liveness probe (left stick X through a
  `linear` pinned to constant 1): pad alive → probe is 1, pad dead → probe is
  nan → the whole `and` goes nan → the channel falls to its 172 failsafe and
  the car disarms.

After a dropout the mapper-side toggle may still be ON; the transmitted value
returns high when the pad reconnects. Whether the car actually re-arms then is
the firmware arm-gate's decision (its failsafe recovery policy), not the
mapper's — but press TRIANGLE once after any dropout to re-sync the toggle
state regardless.

## SDL layout caveat — one bench check owed

Button indices (square 0, cross 1, circle 2, triangle 3, L1 4, R1 5, SHARE 8,
OPTIONS 9) are stable across SDL's HIDAPI and DirectInput paths for the DS4.
Axis indices are **not**: the profile binds the SDL HIDAPI layout (LX 0, LY 1,
RX 2, RY 3, L2 4, R2 5); the legacy DirectInput path instead reports L2=3,
R2=4, RY=5. Before first drive, open the mapper UI, wiggle each stick and
trigger, and confirm the indices — if the right stick Y shows on axis 5, remap
the three axis `number`s (tilt→5, L2→3, R2→4). This is the same class of
recorded validation debt as the wheel bench check.
