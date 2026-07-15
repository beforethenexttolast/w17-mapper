// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"encoding/json"
	"math"
)

// ProtocolVersion is the only supported W3 head-intent protocol version.
// Missing protocol_version is treated as this version during the bench phase
// (contract section 3; owner CB1 item 4).
const ProtocolVersion = 1

// DefaultStaleMs is the canonical receive-time stale authority in milliseconds
// (contract section 3 "Canonical stale boundary"; ratified rev 84532ed). Age
// strictly greater than this is stale: 299/300 fresh, 301 stale.
const DefaultStaleMs int64 = 300

// MaxPacketBytes rejects absurd datagrams before JSON parsing. Real packets are
// ~200 bytes; the input is unauthenticated.
const MaxPacketBytes = 2048

// Rejection reasons. Machine-readable; identical strings to the JS reference so
// diagnostics line up on both sides.
const (
	ReasonOversized        = "oversized"
	ReasonMalformedJSON    = "malformed-json"
	ReasonNotObject        = "not-object"
	ReasonBadVersion       = "bad-version"
	ReasonBadSeq           = "bad-seq"
	ReasonBadTimestamp     = "bad-timestamp"
	ReasonBadAngles        = "bad-angles"
	ReasonOutOfRange       = "out-of-range"
	ReasonBadTrackingEnbld = "bad-tracking-enabled"
	ReasonBadCentered      = "bad-centered"
	ReasonBadCalibrated    = "bad-calibrated"
	ReasonBadTimeout       = "bad-timeout"
)

// Packet is a normalized, validated head-intent packet. Optional booleans and
// timeout_ms are pointers so "absent" is distinguishable from false/zero.
type Packet struct {
	ProtocolVersion int
	Seq             int64
	TimestampMs     int64
	YawDeg          float64
	PitchDeg        float64
	RollDeg         float64
	TrackingEnabled bool
	Centered        *bool // nil if absent/null
	Calibrated      *bool // nil if absent/null (tolerated diagnostic; not in schema)
	TimeoutMs       *int  // nil if absent/null; diagnostic only
}

// jsonInt reports whether x is a JSON *integer* number and returns its value.
// JSON booleans decode to Go bool (not float64), so they never pass — matching
// the reference rule that booleans are not integers.
func jsonInt(x interface{}) (int64, bool) {
	f, ok := x.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

// jsonFinite reports whether x is a finite JSON number.
func jsonFinite(x interface{}) (float64, bool) {
	f, ok := x.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// present reports whether key is set to a non-null value, returning that value.
func present(m map[string]interface{}, key string) (interface{}, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// Validate parses and validates one raw datagram against the canonical W3
// contract. It returns the normalized packet, or a machine-readable reason with
// ok=false. It never panics. Ranges: |yaw|<=360, |pitch|<=180, |roll|<=180;
// timeout_ms 1..5000; seq/timestamp_ms integers >= 0. protocol_version, if
// present, must equal 1.
func Validate(raw []byte) (pkt Packet, reason string, ok bool) {
	if len(raw) > MaxPacketBytes {
		return Packet{}, ReasonOversized, false
	}

	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Packet{}, ReasonMalformedJSON, false
	}
	m, isMap := v.(map[string]interface{})
	if !isMap {
		// Rejects null, arrays, and scalars.
		return Packet{}, ReasonNotObject, false
	}

	// protocol_version: optional; missing/null => version 1.
	version := ProtocolVersion
	if pv, has := present(m, "protocol_version"); has {
		n, isInt := jsonInt(pv)
		if !isInt || n != ProtocolVersion {
			return Packet{}, ReasonBadVersion, false
		}
		version = int(n)
	}

	seq, isInt := jsonInt(m["seq"])
	if !isInt || seq < 0 {
		return Packet{}, ReasonBadSeq, false
	}
	ts, isInt := jsonInt(m["timestamp_ms"])
	if !isInt || ts < 0 {
		return Packet{}, ReasonBadTimestamp, false
	}

	yaw, okY := jsonFinite(m["yaw_deg"])
	pitch, okP := jsonFinite(m["pitch_deg"])
	roll, okR := jsonFinite(m["roll_deg"])
	if !okY || !okP || !okR {
		return Packet{}, ReasonBadAngles, false
	}
	if math.Abs(yaw) > 360 || math.Abs(pitch) > 180 || math.Abs(roll) > 180 {
		return Packet{}, ReasonOutOfRange, false
	}

	enabled, okE := m["tracking_enabled"].(bool)
	if !okE {
		return Packet{}, ReasonBadTrackingEnbld, false
	}

	var centered *bool
	if c, has := present(m, "centered"); has {
		b, okB := c.(bool)
		if !okB {
			return Packet{}, ReasonBadCentered, false
		}
		centered = &b
	}
	var calibrated *bool
	if c, has := present(m, "calibrated"); has {
		b, okB := c.(bool)
		if !okB {
			return Packet{}, ReasonBadCalibrated, false
		}
		calibrated = &b
	}

	var timeoutMs *int
	if t, has := present(m, "timeout_ms"); has {
		n, isInt := jsonInt(t)
		if !isInt || n < 1 || n > 5000 {
			return Packet{}, ReasonBadTimeout, false
		}
		vv := int(n)
		timeoutMs = &vv
	}

	return Packet{
		ProtocolVersion: version,
		Seq:             seq,
		TimestampMs:     ts,
		YawDeg:          yaw,
		PitchDeg:        pitch,
		RollDeg:         roll,
		TrackingEnabled: enabled,
		Centered:        centered,
		Calibrated:      calibrated,
		TimeoutMs:       timeoutMs,
	}, "", true
}
