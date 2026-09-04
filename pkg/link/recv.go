// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package link

import (
	"errors"
	"fmt"
	"github.com/kaack/elrs-joystick-control/pkg/crossfire"
	telem "github.com/kaack/elrs-joystick-control/pkg/crossfire/telemetry"
	"github.com/kaack/elrs-joystick-control/pkg/serial"
	"gopkg.in/tomb.v2"
	"time"
)

// frameReader is the telemetry source the recv loop consumes. *telem.Reader is
// the production implementation. W17 fork addition: the interface exists so the
// loop's TIMING and LIFECYCLE rules -- the keepalive threshold and the
// tomb-armed sends below -- can be tested without a serial port, which is
// otherwise impossible: telem.Reader reads through serial.Port, whose handle is
// unexported and only obtainable by opening a real device.
type frameReader interface {
	Next(t *tomb.Tomb) (telem.TelemType, error)
}

// recvClock is the recv loop's source of time: the tick that paces it and the
// "now" its two inactivity comparisons are made against. W17 fork addition
// (branch B).
//
// WHY IT EXISTS. maxInactivityTime is 2 ms at 921600 baud (four 500 us refresh
// periods), and the keepalive rule is a comparison between that and elapsed
// WALL time. Under `go test`, an ordinary goroutine reschedule or a GC pause is
// routinely longer than 2 ms, so a test that drives the real loop for a real
// 100 ms and then asserts "no keepalive fired" is asserting that the Go
// scheduler never paused this goroutine for 2 ms -- which it does not promise.
// TestKeepaliveStaysQuietWhileTelemetryFlows failed roughly two runs in three
// under `go test -race -count=20 ./pkg/link` on this Mac for exactly that
// reason, with 1-2 keepalives against an expected 0. That is a defect in the
// test, not in the loop: the loop's decision is correct at every instant it is
// actually scheduled.
//
// Injecting the clock lets the timing test step time itself, so the assertion
// becomes "given these instants, the loop asks for no keepalive" -- a property
// of the rule, decided by arithmetic and not by the scheduler.
//
// It changes nothing in production: recvLoop builds a wallClock, whose Now is
// time.Now and whose ticks come from a time.Ticker at the same refresh rate the
// loop always used.
type recvClock interface {
	// Now is the instant the loop measures inactivity against.
	Now() time.Time
	// Ticks paces the loop, one receive per refresh period.
	Ticks() <-chan time.Time
	// Stop releases the tick source. Called once, on loop exit.
	Stop()
}

// wallClock is the production recvClock: real time, real ticker.
type wallClock struct {
	ticker *time.Ticker
}

func newWallClock(refreshRate time.Duration) *wallClock {
	return &wallClock{ticker: time.NewTicker(refreshRate)}
}

func (w *wallClock) Now() time.Time          { return time.Now() }
func (w *wallClock) Ticks() <-chan time.Time { return w.ticker.C }
func (w *wallClock) Stop()                   { w.ticker.Stop() }

func (c *Controller) StartRecvLoop(port *serial.Port, sendChan chan any, recvChan chan any) error {

	if c.recvLoopTomb != nil && c.recvLoopTomb.Alive() {
		return errors.New("send loop is already active")
	}

	c.recvLoopTomb = &tomb.Tomb{}
	c.recvLoopTomb.Go(func() error {
		return c.RecvLoop(port, sendChan, recvChan)
	})

	return nil
}

func (c *Controller) StopRecvLoop() error {
	if c.recvLoopTomb == nil || !c.recvLoopTomb.Alive() {
		return nil
	}

	c.recvLoopTomb.Kill(nil)
	if err := c.recvLoopTomb.Wait(); err != nil {
		return err
	}
	return nil
}

// guardedSend hands a value to the send loop, or gives up when this recv loop
// is being torn down. W17 fork modification (MAP-3).
//
// Both of the recv loop's sends used to be bare `sendChan <- x`. When the send
// loop had already exited -- which it does on ANY serial write error, i.e.
// exactly when the transmitter is unplugged -- there was no receiver left, and
// the recv goroutine parked on that send forever. The supervisor's next step is
// StopRecvLoop, whose Wait() then never returns, so the supervisor never
// reached its reconnect iteration and StopSupervisor (and therefore the
// StopLink RPC, and therefore the ground station's STOP button) blocked
// forever too. Every other blocking point in this loop was already tomb-armed;
// these two were not.
//
// It returns false when the loop must exit.
func (c *Controller) guardedSend(sendChan chan any, value any) bool {
	select {
	case sendChan <- value:
		return true
	case <-c.recvLoopTomb.Dying():
		return false
	}
}

// recvLogInterval is the floor between two prints of the SAME recv-loop
// message. W17 fork addition (review finding N9).
//
// What it bounds. MAP-10 made the model-id keepalive live -- its threshold was
// 2 ms at 921600 baud but the units put it at roughly 33 minutes, so in
// practice it never fired. Neither print is a flood on the paths that matter:
// telemetry.Reader.Next BLOCKS in `for count == 0` until bytes arrive, so a
// merely silent port produces no loop iterations at all, and while telemetry
// flows lastRecvTelemTime is refreshed immediately before the next tick.
//
// The reachable burst is narrow and real: a port whose READS error while its
// WRITES still succeed. Next then returns immediately, every tick both requests
// a keepalive and reports a read error, and at a 500 us refresh rate that is up
// to ~2000 iterations a second, three stdout lines each, into the ground
// station's 200-line diagnostics ring (w17-ground-station/main/mapperRunner.js)
// -- which would push every other line out of the operator's view within a
// fraction of a second. Neither the burst nor the cadence under it has been
// measured on a bench: [bench-TBD].
//
// One second per message keeps the first report immediate, keeps a persistent
// fault visible, and keeps the ring readable. The suppressed count is carried
// on the next line so nothing is silently hidden.
const recvLogInterval = time.Second

// throttledPrint is a per-message rate limiter for the recv loop's two stdout
// lines. W17 fork addition (review finding N9).
//
// It is not safe for concurrent use and does not need to be: both instances
// live in recvLoop's own frame and are touched only by that goroutine.
type throttledPrint struct {
	last       time.Time
	suppressed int
}

// due reports whether this message may be printed now, and how many prints of
// it were suppressed since the last one that was allowed.
func (t *throttledPrint) due(now time.Time) (bool, int) {
	if !t.last.IsZero() && now.Sub(t.last) < recvLogInterval {
		t.suppressed++
		return false, 0
	}

	skipped := t.suppressed
	t.suppressed = 0
	t.last = now

	return true, skipped
}

// alsoSuppressed renders the "and N more" tail, or "" when nothing was hidden.
func alsoSuppressed(skipped int) string {
	if skipped == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d more in the last %v)", skipped, recvLogInterval)
}

func (c *Controller) RecvLoop(port *serial.Port, sendChan chan any, recvChan chan any) error {
	return c.recvLoop(port.BaudRate, telem.NewReader(port), sendChan, recvChan)
}

// recvLoop is RecvLoop with its telemetry source injected; see frameReader. It
// runs on the wall clock, exactly as the production loop always has.
func (c *Controller) recvLoop(baudRate int32, reader frameReader, sendChan chan any, recvChan chan any) error {
	return c.recvLoopClocked(baudRate, reader, nil, sendChan, recvChan)
}

// recvLoopClocked is recvLoop with its clock injected too; see recvClock. A nil
// clock means the wall clock, so the production path is unchanged.
func (c *Controller) recvLoopClocked(baudRate int32, reader frameReader, clock recvClock, sendChan chan any, recvChan chan any) error {
	//time.Sleep(5 * time.Second)
	refreshRate := crossfire.GetRefreshRate(baudRate)
	maxInactivityTime := refreshRate * 4
	fmt.Printf("(recv-loop) starting, refresh rate %v, max inactivity: %v\n", refreshRate, maxInactivityTime)
	if clock == nil {
		clock = newWallClock(refreshRate)
	}
	defer clock.Stop()
	ticks := clock.Ticks()

	telemPacketCount := 1
	telemErrorCount := 0
	tickCount := uint64(0)
	currentTickTime := clock.Now()
	lastRecvTelemTime := clock.Now()
	lastSyncReqTime := clock.Now()

	var tPacket telem.TelemType
	var err error

	// W17 fork addition (review finding N9): one rate limiter per message. See
	// recvLogInterval for the burst these bound.
	keepaliveLog := throttledPrint{}
	readErrorLog := throttledPrint{}

	c.recvPacketsCount = 0
	c.errorPacketsCount = 0
Loop:
	for {
		select {
		case <-c.recvLoopTomb.Dying():
			break Loop

		case chData := <-recvChan:
			switch chData.(type) {
			//receive data from the send loop
			default:
				//no-op
			}

		case currentTickTime = <-ticks:
			tickCount += 1

			// W17 fork modification (branch C): use the instant the tick itself
			// carries rather than a fresh clock.Now() call made just after
			// receiving it. Under the real wallClock the two are a scheduling
			// hair apart and it never mattered; under the injected fakeClock
			// (recvClock, branch B) it does -- Ticks() and Now() read the same
			// mutex-guarded field, and nothing serialises "the loop has taken
			// this tick" with "the loop has read Now() for it". A test driving
			// the clock in a tight sequence of tick() calls (see
			// recv_lifecycle_test.go) can advance the clock for the NEXT tick
			// in the gap between those two lines, so this iteration's own
			// "now" could already reflect a later tick -- observed as
			// TestStopReturnsWhileParkedOnTheKeepalive intermittently reading
			// its zero-length leading settle tick as if 3ms had already
			// elapsed, firing the keepalive a tick early and then hanging the
			// test's next tick() send against a loop that had already parked.
			// Every recvClock implementation's Ticks() already carries the
			// exact instant a tick fired -- wallClock's ticker and fakeClock's
			// tick() both set it at the moment of send -- so using it directly
			// removes the second, racy read instead of adding synchronisation
			// to close the gap.
			//
			// W17 fork modification (MAP-10): compare like with like. Both
			// elapsed times used to be divided by time.Millisecond before being
			// compared against maxInactivityTime, which is a Duration in
			// NANOSECONDS -- so the threshold was out by 10^6. At 921600 baud
			// maxInactivityTime is 2 ms, and the keepalive therefore could not
			// fire until roughly 33 MINUTES of silence: in practice, never. That
			// matters most in exactly the state where it is the only thing left
			// that can notice a vanished port, because channel frames are
			// suppressed whenever no config resolves and this model-id write is
			// then the send loop's only traffic (see send.go's model-id branch,
			// which tears the loop down on a write error so the supervisor can
			// reconnect).
			timeSinceLastTelem := currentTickTime.Sub(lastRecvTelemTime)
			timeSinceLastSyncReq := currentTickTime.Sub(lastSyncReqTime)
			if timeSinceLastTelem > maxInactivityTime && timeSinceLastSyncReq > maxInactivityTime {
				if due, skipped := keepaliveLog.due(currentTickTime); due {
					fmt.Printf("(recv-loop) requesting TelemSync lt:%v, ls:%v%s\n",
						timeSinceLastTelem, timeSinceLastSyncReq, alsoSuppressed(skipped))
				}
				lastSyncReqTime = currentTickTime
				if !c.guardedSend(sendChan, SendModelId) {
					break Loop
				}
			}

			if tPacket, err = reader.Next(c.recvLoopTomb); err != nil {
				if _, ok := err.(*telem.InterruptedError); ok {
					//exit silently
					break
				}

				// This limiter deliberately reads clock.Now() rather than currentTickTime: a
				// read error is logged from the iteration's own wall-clock moment, while the
				// keepalive limiter above keys off the tick instant that decided the keepalive.
				// Only stdout throttling depends on either; no counter or send does.
				if due, skipped := readErrorLog.due(clock.Now()); due {
					fmt.Printf("(recv-loop) error reading telemetry data. error: %s%s\n",
						err.Error(), alsoSuppressed(skipped))
				}
				telemErrorCount += 1
				c.errorPacketsCount += 1
				break
			}

			telemPacketCount += 1
			c.recvPacketsCount += 1

			// W17 fork modification (MAP-10): a frame just arrived, so the link
			// is demonstrably alive. lastRecvTelemTime used to be assigned once,
			// at loop start, and never again -- so even with the unit bug fixed
			// the keepalive would have fired forever once the first threshold
			// passed, regardless of how much telemetry was flowing.
			lastRecvTelemTime = clock.Now()

			switch tFrame := (tPacket).(type) {
			case telem.TelemStatusExtType:
				//fmt.Printf("(recv-loop) %s\n", tFrame)
				c.DeviceStatusBroadcaster.Broadcast(tFrame.Proto())
			case telem.TelemSyncType:
				c.TelemetryBroadcaster.Broadcast(tFrame.Proto())
				if !c.guardedSend(sendChan, &tFrame) {
					break Loop
				}
			case telem.TelemGPSType,
				telem.TelemLinkStatsType,           //ELRS only (originates from RX)
				telem.TelemBatteryType,             //ELRS, TBS (originates from FC)
				telem.TelemAttitudeType,            //ELRS, TBS (originates from FC)
				telem.TelemFlightModeType,          //ELRS, TBS (originates from FC)
				telem.TelemLinkTXType,              //TBS only
				telem.TelemLinkRXType,              //TBS only
				telem.TelemBarometerType,           //TBS only
				telem.TelemVariometerType,          //TBS Only
				telem.TelemBarometerVariometerType: // ELRS only
				//fmt.Printf("(recv-loop) %s\n", tFrame)
				c.TelemetryBroadcaster.Broadcast(tFrame.Proto())
			case telem.TelemDeviceInfoExtType:
				//fmt.Printf("(recv-loop) %s\n", tFrame)
				c.DeviceInfoBroadcaster.Broadcast(tFrame.Proto())
			case telem.TelemDeviceSettingsEntryExtType:
				//fmt.Printf("(recv-loop) %s\n", tFrame)
				c.DeviceFieldBroadcaster.Broadcast(tFrame.Proto())
			default:
				//fmt.Printf("(recv-loop) tData: %x\n", tData)
			}
		}

	}
	fmt.Printf("(recv-loop) exiting recv telemetry loop...\n")

	return nil
}
