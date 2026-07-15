// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later
//
// Package headintent is a LOG-ONLY receiver for the iPhone head-tracking
// "intent" UDP datagrams (windows_bridge_contract.md section 3; default port
// 5602). It is an owned-fork addition to elrs-joystick-control for the W17
// project; it is NOT part of upstream.
//
// SAFETY BOUNDARY (non-negotiable, first milestone):
//
// This package's ONLY outputs are log lines and a read-only diagnostics
// snapshot. It is a deliberate dead end. It does NOT — and must never —
// import or reach the channel mixer (pkg/config), the CRSF/link path
// (pkg/link, pkg/crossfire, pkg/serial), the node graph, the [16]CRSFValue
// arrays, CRSF channels 9/10, firmware, servos, or the gimbal. No value it
// receives may influence any transmitted output. Enabling, disabling, or
// feeding this receiver valid / invalid / stale packets produces no change of
// any kind in the CRSF the mapper transmits.
//
// It carries NO hybrid/rate mapping, NO sign flips, NO endpoint conversion,
// NO arming, NO arbitration, NO return-to-center, and NO active pan/tilt.
// Mapping head intent to camera pan/tilt is a separate, later, safety-gated
// milestone.
//
// The freshness authority is LOCAL MONOTONIC RECEIVE TIME (300 ms). The
// packet's own timeout_ms is a diagnostic hint only and can never weaken that
// threshold. Semantics are ported 1:1 from the reviewed Windows reference
// implementation w17-ground-station/shared/headTracking.js so both sides agree.
package headintent
