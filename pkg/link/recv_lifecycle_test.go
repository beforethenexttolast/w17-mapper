// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package link

// Lifecycle and timing tests for the recv loop (MAP-3 and MAP-10).
//
// MAP-3, the wedge: both of the recv loop's sends to the send loop were bare
// `sendChan <- x` on an unbuffered channel. The send loop exits on ANY serial
// write error -- which is exactly what happens when the transmitter is
// unplugged -- and the supervisor's very next step is StopRecvLoop. With no
// receiver left, the recv goroutine was parked on that send forever, so
// Wait() never returned: the supervisor never reached its reconnect iteration,
// StopSupervisor never returned, and the StopLink RPC behind the ground
// station's STOP button blocked forever. Every OTHER blocking point in the
// loop was already tomb-armed.
//
// MAP-10, the keepalive that could not fire: elapsed time was divided by
// time.Millisecond before being compared against maxInactivityTime, a Duration
// in nanoseconds, so the threshold was out by 10^6 -- about 33 minutes at
// 921600 baud instead of 2 ms. And lastRecvTelemTime was assigned once at loop
// start and never updated, so once that threshold did pass the keepalive would
// have fired on every tick forever, however much telemetry was flowing.
//
// The two interact, which is why they land together: the unit bug is what kept
// the recv.go keepalive send from ever running, and therefore what masked half
// of MAP-3.
//
// These drive the loop through an injected frameReader (see recv.go) rather
// than a serial port: telem.Reader reads through serial.Port, whose handle is
// unexported and only obtainable by opening a real device, and no unattended
// session may open one.

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaack/elrs-joystick-control/pkg/crossfire"
	telem "github.com/kaack/elrs-joystick-control/pkg/crossfire/telemetry"
	"gopkg.in/tomb.v2"
)

// testBaudRate gives a 500 us refresh rate and therefore a 2 ms inactivity
// threshold -- the real race-day numbers, and fast enough to assert on.
const testBaudRate = 921600

// syncFrame is a well-formed OpenTX sync frame: Rate() reads bytes 6..10 and
// Offset() 10..14, so a 16-byte buffer satisfies every accessor Proto() uses.
func syncFrame() telem.TelemSyncType {
	return &telem.SyncExtFrame{RawData: make([]uint8, 16)}
}

// fakeReader feeds the loop whatever the test wants, and counts reads.
type fakeReader struct {
	reads  atomic.Int64
	frame  func() telem.TelemType
	failed error
}

func (f *fakeReader) Next(*tomb.Tomb) (telem.TelemType, error) {
	f.reads.Add(1)
	if f.failed != nil {
		return nil, f.failed
	}
	return f.frame(), nil
}

func newController() *Controller {
	return &Controller{
		portState:               PortUnknown,
		supervisorState:         SupervisorInactive,
		TelemetryBroadcaster:    NewTelemetryBroadcaster(),
		DeviceInfoBroadcaster:   NewTelemetryBroadcaster(),
		DeviceFieldBroadcaster:  NewTelemetryBroadcaster(),
		DeviceStatusBroadcaster: NewTelemetryBroadcaster(),
	}
}

// startRecv runs the recv loop against a reader, with NO ONE reading sendChan
// -- the dead-send-loop state.
func startRecv(t *testing.T, c *Controller, reader frameReader, sendChan chan any) {
	t.Helper()

	c.recvLoopTomb = &tomb.Tomb{}
	c.recvLoopTomb.Go(func() error {
		return c.recvLoop(testBaudRate, reader, sendChan, make(chan any))
	})
}

// fakeClock is a recvClock the test steps by hand. W17 fork addition (branch B).
//
// Every instant the loop measures comes from here, and every tick is delivered
// by an unbuffered send -- which the loop can only take once it has finished the
// previous tick. So a test written against it states a property of the rule
// ("given these instants, no keepalive is due") instead of a property of the Go
// scheduler ("this goroutine was never paused for 2 ms"), which is what made
// TestKeepaliveStaysQuietWhileTelemetryFlows flaky.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	ticks chan time.Time
}

func newFakeClock() *fakeClock {
	// A fixed, arbitrary epoch: nothing here depends on the wall clock.
	return &fakeClock{now: time.Unix(1700000000, 0), ticks: make(chan time.Time)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Ticks() <-chan time.Time { return f.ticks }

func (f *fakeClock) Stop() {}

// tick advances the clock by d and delivers one tick, blocking until the loop
// takes it. It fails the test rather than hanging if the loop has gone away.
func (f *fakeClock) tick(t *testing.T, d time.Duration) {
	t.Helper()

	f.mu.Lock()
	f.now = f.now.Add(d)
	at := f.now
	f.mu.Unlock()

	select {
	case f.ticks <- at:
	case <-time.After(2 * time.Second):
		t.Fatalf("the recv loop did not take a tick within 2s -- it is not running")
	}
}

// settle delivers a zero-length tick, which the loop can only take once it has
// finished the tick before it. It is how a test knows the previous tick was
// fully processed -- including its keepalive send -- before it stops the loop.
// Zero length so the settle tick can never itself trip the inactivity rule:
// both elapsed times are unchanged by it.
func (f *fakeClock) settle(t *testing.T) {
	t.Helper()
	f.tick(t, 0)
}

// startRecvClocked runs the recv loop against an injected reader AND an
// injected clock, so the test drives both inputs of the keepalive rule.
//
// It returns only after a leading settle tick, which the loop can take only once
// it has read its three starting timestamps off this clock. Without that
// handshake a test that advances the clock immediately can have its advance land
// BEFORE the loop initialises, so the loop starts already at the advanced
// instant and measures zero elapsed time -- the trap this helper exists to close.
func startRecvClocked(t *testing.T, c *Controller, reader frameReader, clk *fakeClock, sendChan chan any) {
	t.Helper()

	c.recvLoopTomb = &tomb.Tomb{}
	c.recvLoopTomb.Go(func() error {
		return c.recvLoopClocked(testBaudRate, reader, clk, sendChan, make(chan any))
	})
	clk.settle(t)
}

// stopWithin requires StopRecvLoop to return inside d. Before the fix it never
// returned at all, so a bounded wait is the whole assertion.
func stopWithin(t *testing.T, c *Controller, d time.Duration, what string) {
	t.Helper()

	stopped := make(chan error, 1)
	go func() { stopped <- c.StopRecvLoop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("StopRecvLoop: %v", err)
		}
	case <-time.After(d):
		t.Fatalf("StopRecvLoop did not return within %v while %s -- the recv loop "+
			"is wedged on a send to a dead send loop, and StopLink is wedged behind it", d, what)
	}
}

// TestStopReturnsWhileParkedOnATelemetryFrame is the MAP-3 gate for the
// telemetry-forwarding send (recv.go's TelemSyncType branch).
func TestStopReturnsWhileParkedOnATelemetryFrame(t *testing.T) {
	c := newController()
	reader := &fakeReader{frame: func() telem.TelemType { return syncFrame() }}

	// Unbuffered and unread: the send loop is dead.
	startRecv(t, c, reader, make(chan any))

	// Same park, on the other send: reads stop advancing once the loop is
	// handing a sync frame to a send loop that will never take it.
	waitFor(t, func() bool {
		before := reader.reads.Load()
		time.Sleep(10 * time.Millisecond)
		return before > 0 && reader.reads.Load() == before
	}, "the recv loop to park handing over a telemetry frame")

	stopWithin(t, c, 2*time.Second, "parked handing a sync frame to the send loop")
}

// TestStopReturnsWhileParkedOnTheKeepalive is the MAP-3 gate for the other
// send, the model-id keepalive -- the one that only became reachable once
// MAP-10's unit bug was fixed.
func TestStopReturnsWhileParkedOnTheKeepalive(t *testing.T) {
	c := newController()
	// Every read fails, so no frame ever refreshes lastRecvTelemTime and the
	// keepalive threshold is crossed within a few ticks.
	reader := &fakeReader{failed: errTestRead{}}

	startRecv(t, c, reader, make(chan any))

	// The loop parks on the keepalive send, so its read count STOPS advancing.
	// 10 ms is 20 refresh periods: a loop still running would have read again.
	waitFor(t, func() bool {
		before := reader.reads.Load()
		time.Sleep(10 * time.Millisecond)
		return before > 0 && reader.reads.Load() == before
	}, "the recv loop to park on the model-id keepalive")

	stopWithin(t, c, 2*time.Second, "parked on the model-id keepalive")
}

// TestKeepaliveFiresAfterFourRefreshPeriods is the MAP-10 gate. With the unit
// bug the threshold was ~33 minutes at this baud rate, so this test would time
// out; with like-unit comparison it fires in single-digit milliseconds.
func TestKeepaliveFiresAfterFourRefreshPeriods(t *testing.T) {
	c := newController()
	reader := &fakeReader{failed: errTestRead{}}

	sendChan := make(chan any, 4)
	startRecv(t, c, reader, sendChan)
	defer func() { _ = c.StopRecvLoop() }()

	select {
	case got := <-sendChan:
		if got != SendModelId {
			t.Errorf("the keepalive must be a model-id request, got %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive within 2s: maxInactivityTime is 2ms at 921600 baud, " +
			"so a threshold this loop cannot reach means the units disagree again")
	}
}

// TestKeepaliveStaysQuietWhileTelemetryFlows is the other half of MAP-10:
// every decoded frame must refresh lastRecvTelemTime. Without that assignment
// the keepalive fires on EVERY tick once the first threshold passes -- 2000
// pointless model-id writes a second on a link that is working perfectly.
//
// W17 fork modification (branch B): this used to sleep a real 100 ms and then
// require exactly zero keepalives. maxInactivityTime is 2 ms at this baud rate,
// so that asserted the Go scheduler never paused the loop goroutine for 2 ms --
// which it does not promise. It failed about two runs in three under
// `go test -race -count=20 ./pkg/link` on this Mac, reporting 1-2 keepalives.
// The rule was never wrong; the measurement was. It now steps a fake clock one
// refresh period at a time, so elapsed time between frames is 500 us BY
// CONSTRUCTION and the assertion is arithmetic.
func TestKeepaliveStaysQuietWhileTelemetryFlows(t *testing.T) {
	c := newController()
	reader := &fakeReader{frame: func() telem.TelemType { return syncFrame() }}

	refreshRate := crossfire.GetRefreshRate(testBaudRate)

	// 200 refresh periods -- the same span the wall-clock version slept for, and
	// 50 inactivity thresholds: with a stale timestamp the count would be in the
	// hundreds. Buffered past the worst case so the loop never parks on a send
	// (each tick forwards one sync frame), which would deadlock against the
	// unbuffered tick.
	const ticks = 200
	sendChan := make(chan any, 2*ticks+8)

	clk := newFakeClock()
	startRecvClocked(t, c, reader, clk, sendChan)

	for i := 0; i < ticks; i++ {
		clk.tick(t, refreshRate)
	}
	clk.settle(t)

	if err := c.StopRecvLoop(); err != nil {
		t.Fatalf("StopRecvLoop: %v", err)
	}
	close(sendChan)

	keepalives := 0
	for v := range sendChan {
		if v == SendModelId {
			keepalives++
		}
	}

	if got := reader.reads.Load(); got < ticks {
		t.Fatalf("setup: the loop only read %d frames over %d ticks, too few to conclude anything",
			got, ticks)
	}
	if keepalives != 0 {
		t.Errorf("%d keepalives were sent while telemetry was flowing on every "+
			"tick -- lastRecvTelemTime is not being refreshed", keepalives)
	}
}

// TestKeepaliveFiresWhenTheClockShowsSilence is the anti-vacuity guard for the
// test above, and the reason the fake clock has to be wired into the loop
// rather than merely handed to it. W17 fork addition (branch B).
//
// A fake clock that the loop ignored -- or one that never advanced -- would make
// "no keepalive fired" true for the wrong reason. Here the same fake clock jumps
// past the inactivity threshold with no frame arriving, and the loop must ask
// for a keepalive on the very first tick. Reads FAIL in this one, so
// lastRecvTelemTime is never refreshed and the outcome does not depend on which
// side of the loop's trailing timestamp write the test's advance lands.
func TestKeepaliveFiresWhenTheClockShowsSilence(t *testing.T) {
	c := newController()
	reader := &fakeReader{failed: errTestRead{}}

	maxInactivityTime := crossfire.GetRefreshRate(testBaudRate) * 4

	sendChan := make(chan any, 8)
	clk := newFakeClock()
	startRecvClocked(t, c, reader, clk, sendChan)

	// One tick, one threshold and a half of silence, then a zero-length settle
	// tick so the keepalive send has certainly happened before the stop -- a
	// stop racing that send would let guardedSend take its Dying branch instead.
	clk.tick(t, maxInactivityTime+maxInactivityTime/2)
	clk.settle(t)

	if err := c.StopRecvLoop(); err != nil {
		t.Fatalf("StopRecvLoop: %v", err)
	}
	close(sendChan)

	keepalives := 0
	for v := range sendChan {
		if v == SendModelId {
			keepalives++
		}
	}

	if keepalives != 1 {
		t.Errorf("%d keepalives after %v of silence on the injected clock, want 1 -- "+
			"the loop is not measuring inactivity against the clock it was given",
			keepalives, maxInactivityTime+maxInactivityTime/2)
	}
}

// TestSendChanIsBuffered pins the defence in depth the supervisor sets up.
func TestSendChanIsBuffered(t *testing.T) {
	if sendChanBuffer < 1 {
		t.Errorf("sendChanBuffer = %d, want at least 1", sendChanBuffer)
	}
}

type errTestRead struct{}

func (errTestRead) Error() string { return "test: no telemetry" }

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
