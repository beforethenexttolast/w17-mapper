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
	"sync/atomic"
	"testing"
	"time"

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
func TestKeepaliveStaysQuietWhileTelemetryFlows(t *testing.T) {
	c := newController()
	reader := &fakeReader{frame: func() telem.TelemType { return syncFrame() }}

	// Drained continuously, so a live send loop is simulated and the loop is
	// never parked -- only the keepalive decision is under test.
	sendChan := make(chan any, 64)
	keepalives := &atomic.Int64{}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for v := range sendChan {
			if v == SendModelId {
				keepalives.Add(1)
			}
		}
	}()

	startRecv(t, c, reader, sendChan)

	// 100 ms is 200 refresh periods and 50 inactivity thresholds: with a stale
	// timestamp the keepalive count would be in the hundreds.
	time.Sleep(100 * time.Millisecond)

	if err := c.StopRecvLoop(); err != nil {
		t.Fatalf("StopRecvLoop: %v", err)
	}
	close(sendChan)
	<-drained

	if reader.reads.Load() < 10 {
		t.Fatalf("setup: the loop only read %d frames, too few to conclude anything",
			reader.reads.Load())
	}
	if got := keepalives.Load(); got != 0 {
		t.Errorf("%d keepalives were sent while telemetry was flowing on every "+
			"tick -- lastRecvTelemTime is not being refreshed", got)
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
