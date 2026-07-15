// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"sync"
	"time"
)

// Packet-derived diagnostic states (contract section 3). The receiver adds
// "disabled" and "fault" on top of these; this monitor derives the rest.
const (
	StateIdle          = "idle"            // no valid packet ever received
	StateInvalid       = "invalid"         // only invalid packets ever received
	StateStale         = "stale"           // had a valid packet, silent > staleMs
	StateInactive      = "inactive"        // fresh valid, tracking_enabled=false
	StateNotCentered   = "not_centered"    // fresh valid, enabled, not centered
	StateActiveLogOnly = "active_log_only" // fresh valid, enabled, centered (STILL no output)
)

// Counts of datagrams seen.
type Counts struct {
	Total   uint64
	Valid   uint64
	Invalid uint64
}

// Diagnostics is the read-only snapshot the monitor exposes. It is the ONLY
// data derived from received packets that ever leaves this package.
type Diagnostics struct {
	State           string
	Counts          Counts
	InvalidByReason map[string]uint64
	LastValid       *Packet
	PacketAgeMs     *int64 // nil if no valid packet yet
	// SenderClockDeltaMs = receive-time(ms) - packet timestamp_ms. Diagnostic
	// only; receive time is the stale authority, no clock sync assumed.
	SenderClockDeltaMs *int64
	SeqGaps            uint64
	SeqRegressions     uint64
	SeqRepeats         uint64
	RatePerSec         int
	StaleMs            int64
	// LastError is the receiver's most recent socket/config error text, or "".
	// The monitor never sets it; the Receiver fills it in Diagnostics().
	LastError string
}

// IngestResult reports what happened to one datagram.
type IngestResult struct {
	Accepted bool
	Reason   string // set when Accepted is false
	State    string
}

// Monitor validates datagrams and derives diagnostic state. Safe for concurrent
// use: the receiver goroutine calls Ingest while callers read Diagnostics/State.
type Monitor struct {
	mu              sync.Mutex
	staleMs         int64
	counts          Counts
	invalidByReason map[string]uint64
	lastValid       *Packet
	lastValidRx     time.Time
	hasValid        bool
	seqGaps         uint64
	seqRegressions  uint64
	seqRepeats      uint64
	rxTimes         []time.Time // recent valid receive times for the ~1s rate
}

// NewMonitor builds a monitor. staleMs <= 0 uses DefaultStaleMs.
func NewMonitor(staleMs int64) *Monitor {
	if staleMs <= 0 {
		staleMs = DefaultStaleMs
	}
	return &Monitor{staleMs: staleMs, invalidByReason: map[string]uint64{}}
}

// Ingest validates one datagram received at time now. Invalid packets bump
// counters only — they never replace the last valid packet (contract "Malformed
// Packet Rejection").
func (m *Monitor) Ingest(raw []byte, now time.Time) IngestResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.counts.Total++
	pkt, reason, ok := Validate(raw)
	if !ok {
		m.counts.Invalid++
		m.invalidByReason[reason]++
		return IngestResult{Accepted: false, Reason: reason, State: m.stateLocked(now)}
	}

	// Sequence diagnostics only (a sender restart legitimately resets seq).
	if m.hasValid {
		prev := m.lastValid.Seq
		switch {
		case pkt.Seq == prev:
			m.seqRepeats++
		case pkt.Seq < prev:
			m.seqRegressions++
		case pkt.Seq > prev+1:
			m.seqGaps++
		}
	}

	m.counts.Valid++
	p := pkt
	m.lastValid = &p
	m.lastValidRx = now
	m.hasValid = true
	m.rxTimes = append(m.rxTimes, now)
	m.trimRateWindowLocked(now)

	return IngestResult{Accepted: true, State: m.stateLocked(now)}
}

// State returns the packet-derived state at time now.
func (m *Monitor) State(now time.Time) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked(now)
}

func (m *Monitor) stateLocked(now time.Time) string {
	if !m.hasValid {
		if m.counts.Invalid > 0 {
			return StateInvalid
		}
		return StateIdle
	}
	if now.Sub(m.lastValidRx).Milliseconds() > m.staleMs {
		return StateStale
	}
	if !m.lastValid.TrackingEnabled {
		return StateInactive
	}
	// "centered" must be explicitly true; a calibrated=false hint also blocks.
	if m.lastValid.Centered == nil || !*m.lastValid.Centered ||
		(m.lastValid.Calibrated != nil && !*m.lastValid.Calibrated) {
		return StateNotCentered
	}
	return StateActiveLogOnly
}

// Diagnostics returns a read-only snapshot at time now.
func (m *Monitor) Diagnostics(now time.Time) Diagnostics {
	m.mu.Lock()
	defer m.mu.Unlock()

	d := Diagnostics{
		State:           m.stateLocked(now),
		Counts:          m.counts,
		InvalidByReason: map[string]uint64{},
		SeqGaps:         m.seqGaps,
		SeqRegressions:  m.seqRegressions,
		SeqRepeats:      m.seqRepeats,
		StaleMs:         m.staleMs,
	}
	for k, v := range m.invalidByReason {
		d.InvalidByReason[k] = v
	}
	if m.hasValid {
		p := *m.lastValid
		d.LastValid = &p
		age := now.Sub(m.lastValidRx).Milliseconds()
		d.PacketAgeMs = &age
		delta := m.lastValidRx.UnixMilli() - p.TimestampMs
		d.SenderClockDeltaMs = &delta
	}
	d.RatePerSec = m.rateLocked(now)
	return d
}

// trimRateWindowLocked drops receive times older than 1s before now.
func (m *Monitor) trimRateWindowLocked(now time.Time) {
	cut := now.Add(-time.Second)
	i := 0
	for i < len(m.rxTimes) && m.rxTimes[i].Before(cut) {
		i++
	}
	if i > 0 {
		m.rxTimes = m.rxTimes[i:]
	}
}

// rateLocked counts valid packets received within the last second.
func (m *Monitor) rateLocked(now time.Time) int {
	cut := now.Add(-time.Second)
	n := 0
	for _, t := range m.rxTimes {
		if !t.Before(cut) {
			n++
		}
	}
	return n
}
