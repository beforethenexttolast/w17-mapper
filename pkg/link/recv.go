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

func (c *Controller) RecvLoop(port *serial.Port, sendChan chan any, recvChan chan any) error {
	return c.recvLoop(port.BaudRate, telem.NewReader(port), sendChan, recvChan)
}

// recvLoop is RecvLoop with its telemetry source injected; see frameReader.
func (c *Controller) recvLoop(baudRate int32, reader frameReader, sendChan chan any, recvChan chan any) error {
	//time.Sleep(5 * time.Second)
	refreshRate := crossfire.GetRefreshRate(baudRate)
	maxInactivityTime := refreshRate * 4
	fmt.Printf("(recv-loop) starting, refresh rate %v, max inactivity: %v\n", refreshRate, maxInactivityTime)
	ticker := time.NewTicker(refreshRate)
	defer ticker.Stop()

	telemPacketCount := 1
	telemErrorCount := 0
	tickCount := uint64(0)
	currentTickTime := time.Now()
	lastRecvTelemTime := time.Now()
	lastSyncReqTime := time.Now()

	var tPacket telem.TelemType
	var err error

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

		case <-ticker.C:
			tickCount += 1
			currentTickTime = time.Now()

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
				fmt.Printf("(recv-loop) requesting TelemSync lt:%v, ls:%v\n", timeSinceLastTelem, timeSinceLastSyncReq)
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

				fmt.Printf("(recv-loop) error reading telemetry data. error: %s\n", err.Error())
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
			lastRecvTelemTime = time.Now()

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
