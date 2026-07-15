// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

func baseValid() map[string]interface{} {
	return map[string]interface{}{
		"seq":              1,
		"timestamp_ms":     1000,
		"yaw_deg":          -12.5,
		"pitch_deg":        6.8,
		"roll_deg":         1.2,
		"tracking_enabled": true,
		"centered":         true,
		"timeout_ms":       250,
	}
}

func mustJSON(t *testing.T, m map[string]interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestValidateAcceptsCanonicalPacket(t *testing.T) {
	pkt, reason, ok := Validate(mustJSON(t, baseValid()))
	if !ok {
		t.Fatalf("expected accept, got reason %q", reason)
	}
	if pkt.ProtocolVersion != 1 {
		t.Errorf("version = %d, want 1", pkt.ProtocolVersion)
	}
	if pkt.Seq != 1 || pkt.TimestampMs != 1000 {
		t.Errorf("seq/ts = %d/%d", pkt.Seq, pkt.TimestampMs)
	}
	if pkt.YawDeg != -12.5 || pkt.PitchDeg != 6.8 || pkt.RollDeg != 1.2 {
		t.Errorf("angles = %v/%v/%v", pkt.YawDeg, pkt.PitchDeg, pkt.RollDeg)
	}
	if !pkt.TrackingEnabled {
		t.Error("tracking_enabled should be true")
	}
	if pkt.Centered == nil || !*pkt.Centered {
		t.Error("centered should be true")
	}
	if pkt.TimeoutMs == nil || *pkt.TimeoutMs != 250 {
		t.Error("timeout_ms should be 250 (diagnostic)")
	}
}

func TestValidateMissingProtocolVersionIsV1(t *testing.T) {
	m := baseValid() // no protocol_version key
	if _, ok := m["protocol_version"]; ok {
		t.Fatal("base should not carry protocol_version")
	}
	pkt, reason, ok := Validate(mustJSON(t, m))
	if !ok {
		t.Fatalf("expected accept, got %q", reason)
	}
	if pkt.ProtocolVersion != 1 {
		t.Errorf("missing protocol_version should default to 1, got %d", pkt.ProtocolVersion)
	}
}

func TestValidateExplicitProtocolVersion1(t *testing.T) {
	m := baseValid()
	m["protocol_version"] = 1
	if _, reason, ok := Validate(mustJSON(t, m)); !ok {
		t.Fatalf("protocol_version 1 should accept, got %q", reason)
	}
}

func TestValidateOptionalFieldsAbsent(t *testing.T) {
	m := baseValid()
	delete(m, "centered")
	delete(m, "timeout_ms")
	pkt, reason, ok := Validate(mustJSON(t, m))
	if !ok {
		t.Fatalf("optional fields absent should accept, got %q", reason)
	}
	if pkt.Centered != nil || pkt.TimeoutMs != nil {
		t.Error("absent optional fields should normalize to nil")
	}
}

func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]interface{})
		reason string
	}{
		{"missing seq", func(m map[string]interface{}) { delete(m, "seq") }, ReasonBadSeq},
		{"negative seq", func(m map[string]interface{}) { m["seq"] = -1 }, ReasonBadSeq},
		{"fractional seq", func(m map[string]interface{}) { m["seq"] = 1.5 }, ReasonBadSeq},
		{"bool seq (bool!=int)", func(m map[string]interface{}) { m["seq"] = true }, ReasonBadSeq},
		{"missing timestamp", func(m map[string]interface{}) { delete(m, "timestamp_ms") }, ReasonBadTimestamp},
		{"negative timestamp", func(m map[string]interface{}) { m["timestamp_ms"] = -5 }, ReasonBadTimestamp},
		{"missing yaw", func(m map[string]interface{}) { delete(m, "yaw_deg") }, ReasonBadAngles},
		{"string yaw", func(m map[string]interface{}) { m["yaw_deg"] = "0" }, ReasonBadAngles},
		{"yaw out of range", func(m map[string]interface{}) { m["yaw_deg"] = 400.0 }, ReasonOutOfRange},
		{"pitch out of range", func(m map[string]interface{}) { m["pitch_deg"] = 200.0 }, ReasonOutOfRange},
		{"roll out of range", func(m map[string]interface{}) { m["roll_deg"] = -200.0 }, ReasonOutOfRange},
		{"missing tracking_enabled", func(m map[string]interface{}) { delete(m, "tracking_enabled") }, ReasonBadTrackingEnbld},
		{"string tracking_enabled", func(m map[string]interface{}) { m["tracking_enabled"] = "true" }, ReasonBadTrackingEnbld},
		{"non-bool centered", func(m map[string]interface{}) { m["centered"] = "yes" }, ReasonBadCentered},
		{"non-bool calibrated", func(m map[string]interface{}) { m["calibrated"] = 1 }, ReasonBadCalibrated},
		{"timeout too low", func(m map[string]interface{}) { m["timeout_ms"] = 0 }, ReasonBadTimeout},
		{"timeout too high", func(m map[string]interface{}) { m["timeout_ms"] = 6000 }, ReasonBadTimeout},
		{"fractional timeout", func(m map[string]interface{}) { m["timeout_ms"] = 1.5 }, ReasonBadTimeout},
		{"bad version", func(m map[string]interface{}) { m["protocol_version"] = 2 }, ReasonBadVersion},
		{"fractional version", func(m map[string]interface{}) { m["protocol_version"] = 1.5 }, ReasonBadVersion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := baseValid()
			c.mutate(m)
			_, reason, ok := Validate(mustJSON(t, m))
			if ok {
				t.Fatalf("%s: expected rejection %q, got accept", c.name, c.reason)
			}
			if reason != c.reason {
				t.Errorf("%s: reason = %q, want %q", c.name, reason, c.reason)
			}
		})
	}
}

func TestValidateRawRejections(t *testing.T) {
	cases := []struct {
		name   string
		raw    []byte
		reason string
	}{
		{"malformed json", []byte("{"), ReasonMalformedJSON},
		{"empty", []byte(""), ReasonMalformedJSON},
		{"array not object", []byte("[1,2,3]"), ReasonNotObject},
		{"null not object", []byte("null"), ReasonNotObject},
		{"scalar not object", []byte("5"), ReasonNotObject},
		{"oversized", bytes.Repeat([]byte(" "), MaxPacketBytes+1), ReasonOversized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, reason, ok := Validate(c.raw)
			if ok {
				t.Fatalf("%s: expected rejection", c.name)
			}
			if reason != c.reason {
				t.Errorf("%s: reason = %q, want %q", c.name, reason, c.reason)
			}
		})
	}
}

// White-box coverage of the number guards, incl. the NaN/Inf/bool branches that
// JSON encoding cannot express directly.
func TestNumberGuards(t *testing.T) {
	if _, ok := jsonInt(true); ok {
		t.Error("bool must not be an int")
	}
	if _, ok := jsonInt(1.5); ok {
		t.Error("1.5 must not be an int")
	}
	if _, ok := jsonInt(math.Inf(1)); ok {
		t.Error("+Inf must not be an int")
	}
	if v, ok := jsonInt(float64(42)); !ok || v != 42 {
		t.Errorf("42 should be int, got %d,%v", v, ok)
	}
	if _, ok := jsonFinite(math.NaN()); ok {
		t.Error("NaN must not be finite")
	}
	if _, ok := jsonFinite(math.Inf(-1)); ok {
		t.Error("-Inf must not be finite")
	}
	if _, ok := jsonFinite("x"); ok {
		t.Error("string must not be finite")
	}
}
