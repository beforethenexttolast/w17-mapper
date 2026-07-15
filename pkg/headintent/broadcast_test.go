// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"sync"
	"testing"
	"time"
)

// mutClock is a manually advanced clock for deterministic broadcaster tests.
type mutClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mutClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *mutClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// mutSource is a swappable snapshot source.
type mutSource struct {
	mu sync.Mutex
	d  Diagnostics
}

func (s *mutSource) set(d Diagnostics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = d
}
func (s *mutSource) snap() Diagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d
}

func idle(total uint64) Diagnostics {
	return Diagnostics{State: StateIdle, Counts: Counts{Total: total}, StaleMs: DefaultStaleMs}
}

// readNonBlocking returns (value, true) if a snapshot is immediately available.
func readNonBlocking(ch <-chan Diagnostics) (Diagnostics, bool) {
	select {
	case d := <-ch:
		return d, true
	default:
		return Diagnostics{}, false
	}
}

func newTestBroadcaster(src *mutSource, clk *mutClock) *Broadcaster {
	return NewBroadcaster(src.snap, BroadcasterOptions{
		Now:         clk.now,
		MinValueGap: 100 * time.Millisecond,
	})
}

func TestBroadcasterInitialSnapshotOnSubscribe(t *testing.T) {
	src := &mutSource{d: idle(1)}
	b := newTestBroadcaster(src, &mutClock{t: baseTime})

	ch, unsub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsub()

	got, ok := readNonBlocking(ch)
	if !ok {
		t.Fatal("no snapshot delivered immediately on subscribe")
	}
	if got.State != StateIdle || got.Counts.Total != 1 {
		t.Errorf("initial snapshot = %+v, want idle/total=1", got)
	}
}

func TestBroadcasterTransitionBypassesRateLimit(t *testing.T) {
	clk := &mutClock{t: baseTime}
	src := &mutSource{d: idle(1)}
	b := newTestBroadcaster(src, clk)

	ch, unsub, _ := b.Subscribe()
	defer unsub()
	readNonBlocking(ch) // drain the immediate snapshot

	b.pump() // first sample: emits
	readNonBlocking(ch)

	// A state transition only 10ms later (< 100ms MinValueGap) must still emit.
	clk.advance(10 * time.Millisecond)
	src.set(Diagnostics{State: StateStale, Counts: Counts{Total: 1}, StaleMs: DefaultStaleMs})
	b.pump()

	got, ok := readNonBlocking(ch)
	if !ok {
		t.Fatal("state transition was not emitted within the rate-limit window")
	}
	if got.State != StateStale {
		t.Errorf("emitted state = %q, want stale", got.State)
	}
}

func TestBroadcasterValueUpdatesAreRateLimited(t *testing.T) {
	clk := &mutClock{t: baseTime}
	src := &mutSource{d: idle(1)}
	b := newTestBroadcaster(src, clk)

	ch, unsub, _ := b.Subscribe()
	defer unsub()
	readNonBlocking(ch)

	b.pump() // baseline emit
	readNonBlocking(ch)

	// Value-only change (same state), 10ms later: must NOT emit yet.
	clk.advance(10 * time.Millisecond)
	src.set(idle(2))
	b.pump()
	if _, ok := readNonBlocking(ch); ok {
		t.Fatal("value-only update emitted before the 10Hz window elapsed")
	}

	// Once the window elapses, the latest value is emitted.
	clk.advance(100 * time.Millisecond)
	b.pump()
	got, ok := readNonBlocking(ch)
	if !ok {
		t.Fatal("value update not emitted after the rate-limit window")
	}
	if got.Counts.Total != 2 {
		t.Errorf("emitted total = %d, want 2 (latest)", got.Counts.Total)
	}
}

func TestBroadcasterSlowSubscriberKeepsLatestAndNeverBlocks(t *testing.T) {
	clk := &mutClock{t: baseTime}
	src := &mutSource{d: idle(1)}
	b := newTestBroadcaster(src, clk)

	ch, unsub, _ := b.Subscribe()
	defer unsub()
	// Deliberately never drain until the end (a stuck subscriber).

	// Force several emits via state transitions; each pump must return promptly
	// (never block on the full cap-1 channel).
	states := []string{StateIdle, StateInactive, StateNotCentered, StateActiveLogOnly}
	for i, st := range states {
		clk.advance(10 * time.Millisecond)
		src.set(Diagnostics{State: st, Counts: Counts{Total: uint64(i + 1)}, StaleMs: DefaultStaleMs})
		done := make(chan struct{})
		go func() { b.pump(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("pump blocked on a slow subscriber")
		}
	}

	// The stuck subscriber retains only the most recent snapshot.
	got, ok := readNonBlocking(ch)
	if !ok {
		t.Fatal("expected the latest snapshot to be buffered")
	}
	if got.State != StateActiveLogOnly || got.Counts.Total != 4 {
		t.Errorf("buffered snapshot = %+v, want latest active_log_only/total=4", got)
	}
	// And only ONE item was buffered (latest-value, cap 1).
	if _, ok := readNonBlocking(ch); ok {
		t.Fatal("more than one snapshot buffered for a slow subscriber")
	}
}

func TestBroadcasterCapsAtFourSubscribers(t *testing.T) {
	b := NewBroadcaster((&mutSource{d: idle(0)}).snap, BroadcasterOptions{Now: (&mutClock{t: baseTime}).now})

	var unsubs []func()
	for i := 0; i < 4; i++ {
		_, unsub, err := b.Subscribe()
		if err != nil {
			t.Fatalf("subscriber %d rejected: %v", i+1, err)
		}
		unsubs = append(unsubs, unsub)
	}
	if b.SubscriberCount() != 4 {
		t.Fatalf("count = %d, want 4", b.SubscriberCount())
	}

	// Fifth is refused.
	if _, _, err := b.Subscribe(); err != ErrTooManySubscribers {
		t.Fatalf("5th Subscribe err = %v, want ErrTooManySubscribers", err)
	}

	// Releasing one frees a slot.
	unsubs[0]()
	if b.SubscriberCount() != 3 {
		t.Fatalf("count after unsub = %d, want 3", b.SubscriberCount())
	}
	if _, _, err := b.Subscribe(); err != nil {
		t.Fatalf("Subscribe after release: %v", err)
	}
}

func TestBroadcasterUnsubscribeReleases(t *testing.T) {
	b := NewBroadcaster((&mutSource{d: idle(0)}).snap, BroadcasterOptions{Now: (&mutClock{t: baseTime}).now})
	_, unsub, _ := b.Subscribe()
	if b.SubscriberCount() != 1 {
		t.Fatalf("count = %d, want 1", b.SubscriberCount())
	}
	unsub()
	unsub() // idempotent
	if b.SubscriberCount() != 0 {
		t.Fatalf("count after unsub = %d, want 0", b.SubscriberCount())
	}
}

// TestReceiveAgeIsReceiveTimeNotSenderTimestamp proves the mapper computes
// freshness from local receive time, never from the iPhone-supplied timestamp_ms.
func TestReceiveAgeIsReceiveTimeNotSenderTimestamp(t *testing.T) {
	m := NewMonitor(300)
	// timestamp_ms is deliberately tiny/old relative to the receive time.
	pkt := baseValid()
	pkt["timestamp_ms"] = 1000
	m.Ingest(mustJSON(t, pkt), at(5000)) // received at t=5000ms

	d := m.Diagnostics(at(5200)) // observed 200ms later
	if d.PacketAgeMs == nil {
		t.Fatal("no PacketAgeMs")
	}
	if *d.PacketAgeMs != 200 {
		t.Errorf("receive age = %d ms, want 200 (receive-time based, not from timestamp_ms=1000)", *d.PacketAgeMs)
	}
}

// TestLastValidPreservedAcrossInvalidAndStale proves the last valid sample survives
// later invalid packets and the stale transition (contract "malformed rejection").
func TestLastValidPreservedAcrossInvalidAndStale(t *testing.T) {
	m := NewMonitor(300)
	m.Ingest(validPkt(t, 7, true, boolp(true)), at(0)) // valid seq 7

	m.Ingest([]byte("{ not json"), at(50)) // invalid: must not replace last valid
	d := m.Diagnostics(at(50))
	if d.LastValid == nil || d.LastValid.Seq != 7 {
		t.Fatalf("after invalid: lastValid = %+v, want seq 7 preserved", d.LastValid)
	}
	if d.Counts.Invalid != 1 || d.Counts.Valid != 1 {
		t.Errorf("counts = %+v, want valid 1 / invalid 1", d.Counts)
	}

	// Go stale (silent past 300ms): last valid is still preserved.
	d = m.Diagnostics(at(500))
	if d.State != StateStale {
		t.Errorf("state = %q, want stale", d.State)
	}
	if d.LastValid == nil || d.LastValid.Seq != 7 {
		t.Fatalf("after stale: lastValid = %+v, want seq 7 preserved", d.LastValid)
	}
}
