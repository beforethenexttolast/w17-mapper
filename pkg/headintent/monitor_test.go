// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"testing"
	"time"
)

var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(ms int64) time.Time { return baseTime.Add(time.Duration(ms) * time.Millisecond) }

// validPkt builds a canonical packet with the given knobs.
func validPkt(t *testing.T, seq int, enabled bool, centered *bool) []byte {
	t.Helper()
	m := baseValid()
	m["seq"] = seq
	m["tracking_enabled"] = enabled
	if centered == nil {
		delete(m, "centered")
	} else {
		m["centered"] = *centered
	}
	return mustJSON(t, m)
}

func boolp(b bool) *bool { return &b }

func TestInitialStateIdle(t *testing.T) {
	m := NewMonitor(0)
	if got := m.State(at(0)); got != StateIdle {
		t.Errorf("initial state = %q, want idle", got)
	}
}

func TestOnlyInvalidYieldsInvalidState(t *testing.T) {
	m := NewMonitor(0)
	res := m.Ingest([]byte("{"), at(0))
	if res.Accepted {
		t.Fatal("malformed packet should not be accepted")
	}
	if got := m.State(at(0)); got != StateInvalid {
		t.Errorf("state after only-invalid = %q, want invalid", got)
	}
	if m.Diagnostics(at(0)).LastValid != nil {
		t.Error("invalid packet must not set lastValid")
	}
}

func TestInactiveWhenTrackingDisabled(t *testing.T) {
	m := NewMonitor(0)
	m.Ingest(validPkt(t, 1, false, boolp(true)), at(0))
	if got := m.State(at(10)); got != StateInactive {
		t.Errorf("state = %q, want inactive", got)
	}
}

func TestNotCenteredVariants(t *testing.T) {
	t.Run("centered absent", func(t *testing.T) {
		m := NewMonitor(0)
		m.Ingest(validPkt(t, 1, true, nil), at(0))
		if got := m.State(at(10)); got != StateNotCentered {
			t.Errorf("state = %q, want not_centered", got)
		}
	})
	t.Run("centered false", func(t *testing.T) {
		m := NewMonitor(0)
		m.Ingest(validPkt(t, 1, true, boolp(false)), at(0))
		if got := m.State(at(10)); got != StateNotCentered {
			t.Errorf("state = %q, want not_centered", got)
		}
	})
	t.Run("centered true but calibrated false", func(t *testing.T) {
		m := NewMonitor(0)
		mp := baseValid()
		mp["tracking_enabled"] = true
		mp["centered"] = true
		mp["calibrated"] = false
		m.Ingest(mustJSON(t, mp), at(0))
		if got := m.State(at(10)); got != StateNotCentered {
			t.Errorf("state = %q, want not_centered", got)
		}
	})
}

func TestActiveLogOnlyWhenEnabledAndCentered(t *testing.T) {
	m := NewMonitor(0)
	m.Ingest(validPkt(t, 1, true, boolp(true)), at(0))
	if got := m.State(at(10)); got != StateActiveLogOnly {
		t.Errorf("state = %q, want active_log_only", got)
	}
}

// The exact canonical freshness boundary: 299/300 fresh, 301 stale.
func TestFreshnessBoundary(t *testing.T) {
	m := NewMonitor(DefaultStaleMs) // 300
	m.Ingest(validPkt(t, 1, true, boolp(true)), at(0))

	if got := m.State(at(299)); got != StateActiveLogOnly {
		t.Errorf("age 299ms = %q, want active_log_only (fresh)", got)
	}
	if got := m.State(at(300)); got != StateActiveLogOnly {
		t.Errorf("age 300ms = %q, want active_log_only (fresh)", got)
	}
	if got := m.State(at(301)); got != StateStale {
		t.Errorf("age 301ms = %q, want stale", got)
	}
}

func TestInvalidPacketPreservesLastValidState(t *testing.T) {
	m := NewMonitor(0)
	m.Ingest(validPkt(t, 7, true, boolp(true)), at(0))
	// A burst of garbage arrives.
	m.Ingest([]byte("garbage"), at(5))
	m.Ingest(mustJSON(t, map[string]interface{}{"seq": -1}), at(6))

	d := m.Diagnostics(at(10))
	if d.State != StateActiveLogOnly {
		t.Errorf("state after invalid burst = %q, want active_log_only (unchanged)", d.State)
	}
	if d.LastValid == nil || d.LastValid.Seq != 7 {
		t.Errorf("lastValid must remain seq 7, got %+v", d.LastValid)
	}
	if d.Counts.Valid != 1 || d.Counts.Invalid != 2 || d.Counts.Total != 3 {
		t.Errorf("counts = %+v, want valid=1 invalid=2 total=3", d.Counts)
	}
}

func TestSequenceDiagnostics(t *testing.T) {
	m := NewMonitor(0)
	c := boolp(true)
	m.Ingest(validPkt(t, 1, true, c), at(0)) // first: no diag
	m.Ingest(validPkt(t, 2, true, c), at(1)) // contiguous
	m.Ingest(validPkt(t, 5, true, c), at(2)) // gap (jumped 3)
	m.Ingest(validPkt(t, 5, true, c), at(3)) // repeat
	m.Ingest(validPkt(t, 3, true, c), at(4)) // regression

	d := m.Diagnostics(at(5))
	if d.SeqGaps != 1 {
		t.Errorf("gaps = %d, want 1", d.SeqGaps)
	}
	if d.SeqRepeats != 1 {
		t.Errorf("repeats = %d, want 1", d.SeqRepeats)
	}
	if d.SeqRegressions != 1 {
		t.Errorf("regressions = %d, want 1", d.SeqRegressions)
	}
}

func TestCountsAndRate(t *testing.T) {
	m := NewMonitor(0)
	c := boolp(true)
	m.Ingest(validPkt(t, 1, true, c), at(0))
	m.Ingest(validPkt(t, 2, true, c), at(100))
	m.Ingest(validPkt(t, 3, true, c), at(200))
	m.Ingest([]byte("{"), at(250)) // invalid

	d := m.Diagnostics(at(300))
	if d.Counts.Valid != 3 || d.Counts.Invalid != 1 {
		t.Errorf("counts = %+v", d.Counts)
	}
	if d.RatePerSec != 3 {
		t.Errorf("rate = %d, want 3 (all within 1s)", d.RatePerSec)
	}
	// Long after the window, the rate decays to zero (packets age out).
	if late := m.Diagnostics(at(2000)).RatePerSec; late != 0 {
		t.Errorf("rate at 2s = %d, want 0", late)
	}
}

// Output boundary: no sequence of packets may ever produce an authority
// ("active") state, and the observable state is always one of the named
// log-only states. This module exposes only Diagnostics/State — never a channel
// value, callback, or command.
func TestNeverProducesActiveAuthorityState(t *testing.T) {
	allowed := map[string]bool{
		StateIdle: true, StateInvalid: true, StateStale: true,
		StateInactive: true, StateNotCentered: true, StateActiveLogOnly: true,
	}
	m := NewMonitor(DefaultStaleMs)
	c := boolp(true)
	steps := []time.Time{at(0), at(50), at(400) /*stale*/}
	m.Ingest(validPkt(t, 1, true, c), at(0))
	m.Ingest([]byte("bad"), at(10))
	m.Ingest(validPkt(t, 2, false, c), at(50)) // inactive
	for _, ts := range steps {
		s := m.State(ts)
		if s == "active" {
			t.Fatal("monitor must never report the authority state 'active'")
		}
		if !allowed[s] {
			t.Errorf("unexpected state %q", s)
		}
	}
}
