// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package link

// Rate limiting for the recv loop's two stdout lines (review finding N9).
//
// MAP-10 made the model-id keepalive live: its threshold is 2 ms at 921600
// baud, and the unit bug had put it at roughly 33 minutes, so in practice it
// never fired. That is a correction, but it also makes a print reachable that
// effectively never ran.
//
// Neither line floods on the paths that matter -- telemetry.Reader.Next blocks
// until bytes arrive, so a silent port produces no iterations, and while
// telemetry flows the timestamp is refreshed before the next tick. The
// reachable burst is a port whose READS error while its WRITES succeed: every
// tick then both asks for a keepalive and reports a read error, up to ~2000
// iterations a second, into the ground station's 200-line diagnostics ring.
//
// The cadence on real hardware is unmeasured: [bench-TBD].

import (
	"strings"
	"testing"
	"time"
)

// TestThrottledPrintAllowsTheFirstAndBoundsTheRest is the rule itself: the
// first report is immediate (an operator must not wait a second to be told the
// port is failing), and everything inside the window is counted, not printed.
func TestThrottledPrintAllowsTheFirstAndBoundsTheRest(t *testing.T) {
	var log throttledPrint
	base := time.Now()

	if due, skipped := log.due(base); !due || skipped != 0 {
		t.Fatalf("the first report must print immediately; due=%v skipped=%d", due, skipped)
	}

	// 2000 iterations a second is the burst rate arithmetic gives at a 500 us
	// refresh; every one of them INSIDE the window must be counted, not printed.
	for i := 1; i < 2000; i++ {
		if due, _ := log.due(base.Add(time.Duration(i) * 500 * time.Microsecond)); due {
			t.Fatalf("iteration %d printed inside the %v window", i, recvLogInterval)
		}
	}

	// One window later the message returns, carrying what was hidden.
	due, skipped := log.due(base.Add(recvLogInterval))
	if !due {
		t.Fatal("a persistent fault must keep being reported once per window")
	}
	if skipped != 1999 {
		t.Errorf("suppressed count = %d, want 1999 -- nothing may be hidden silently", skipped)
	}

	// And the count resets, so the next line does not re-report old ones.
	if _, skipped := log.due(base.Add(2 * recvLogInterval)); skipped != 0 {
		t.Errorf("suppressed count = %d after a quiet window, want 0", skipped)
	}
}

// TestAlsoSuppressedIsSilentWhenNothingWasHidden keeps the common case clean:
// a single event prints the plain sentence, with no parenthetical.
func TestAlsoSuppressedIsSilentWhenNothingWasHidden(t *testing.T) {
	if got := alsoSuppressed(0); got != "" {
		t.Errorf("alsoSuppressed(0) = %q, want empty", got)
	}
	if got := alsoSuppressed(7); !strings.Contains(got, "7 more") {
		t.Errorf("alsoSuppressed(7) = %q, want it to name the count", got)
	}
}

// TestReadErrorsAreCountedEvenWhenNotPrinted is the over-correction guard, and
// the one that matters for diagnostics: the rate limit governs STDOUT only. The
// error counters the RPC surface reports must still see every failure.
func TestReadErrorsAreCountedEvenWhenNotPrinted(t *testing.T) {
	c := newController()
	reader := &fakeReader{failed: errTestRead{}}

	// Drained, so the loop is never parked on the keepalive send and keeps
	// iterating -- this is the burst state the rate limit exists for.
	sendChan := make(chan any, 64)
	go func() {
		for range sendChan {
		}
	}()

	startRecv(t, c, reader, sendChan)
	time.Sleep(50 * time.Millisecond)

	if err := c.StopRecvLoop(); err != nil {
		t.Fatalf("StopRecvLoop: %v", err)
	}

	if reader.reads.Load() < 10 {
		t.Fatalf("setup: only %d reads, too few to conclude anything", reader.reads.Load())
	}
	if c.errorPacketsCount < 10 {
		t.Errorf("errorPacketsCount = %d after %d failing reads -- the rate limit must "+
			"govern printing, not counting", c.errorPacketsCount, reader.reads.Load())
	}
}
