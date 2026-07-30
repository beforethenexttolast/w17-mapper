// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package link

import (
	"errors"
	"fmt"
	crsf "github.com/kaack/elrs-joystick-control/pkg/crossfire"
	telem "github.com/kaack/elrs-joystick-control/pkg/crossfire/telemetry"
	"github.com/kaack/elrs-joystick-control/pkg/serial"
	"github.com/kaack/elrs-joystick-control/pkg/util"
	"gopkg.in/tomb.v2"
	"time"
)

func (c *Controller) StartSendLoop(port *serial.Port, sendChan chan any, recvChan chan any) error {

	if c.sendLoopTomb != nil && c.sendLoopTomb.Alive() {
		return errors.New("send loop is already active")
	}

	c.sendLoopTomb = &tomb.Tomb{}
	c.sendLoopTomb.Go(func() error {
		return c.SendLoop(port, sendChan, recvChan)
	})

	return nil
}

func (c *Controller) StopSendLoop() error {
	if c.sendLoopTomb == nil || !c.sendLoopTomb.Alive() {
		return nil
	}

	c.sendLoopTomb.Kill(nil)
	if err := c.sendLoopTomb.Wait(); err != nil {
		return err
	}
	return nil
}

// resolveChannels returns the channel array to transmit for a port, or nil when
// the mapper has no config for it. W17 fork addition.
//
// A nil result means "send no channel frame at all", which is a deliberate
// safety choice, not an oversight. Upstream transmitted Controller.EvalNoData
// (all zeros) in this case, at the full CRSF refresh rate, in well-formed
// CRC-valid frames. That is the same class of defect as the hold-last bug fixed
// in the config layer: the receiver sees a perfectly healthy link carrying a
// payload the mapper has no basis for, so no link-loss failsafe can ever fire.
//
// There is no safe payload to invent here. Unlike the stale-input path, "no
// config" means the channel ROLES are unknown, so no per-channel neutral can be
// derived:
//
//   - All zeros is below the nominal 172..1811 CRSF range. A receiver that
//     normalizes against those anchors reads it as FULL NEGATIVE deflection on
//     every channel.
//   - All 992 (center) is worse for switches, not better. A receiver applying
//     hysteresis around center HOLDS the previous switch state, so clearing a
//     config mid-session would leave an arm switch LATCHED ON. This is why the
//     unused EvalCenter constant was removed rather than adopted.
//
// Sending nothing instead routes the condition into the receiver's own
// link-loss failsafe -- the designed, tested mechanism for "no valid control
// input" -- rather than depending on how a particular receiver happens to
// interpret an out-of-band payload.
//
// Note the consequence for port liveness: while suppressed, no channel write
// happens, so a serial disconnect is not detected by the channel write error.
// The periodic model-id write from the recv loop covers this (it tears the loop
// down on error for exactly this reason), and in any case nothing is being
// commanded during this window.
func resolveChannels(channelsDataMap *map[string]*[16]util.CRSFValue, portName string) *[16]util.CRSFValue {
	if channelsDataMap == nil {
		return nil
	}

	// A cleared config leaves this map non-nil but EMPTY, so the miss below is
	// the reachable case, not just the nil one.
	values, ok := (*channelsDataMap)[portName]
	if !ok {
		return nil
	}

	return values
}

//goland:noinspection GoUnusedParameter
func (c *Controller) SendLoop(port *serial.Port, sendChan chan any, recvChan chan any) error {

	currentRefreshRate := crsf.GetRefreshRate(port.BaudRate)
	nextRefreshRate := currentRefreshRate

	fmt.Printf("(send-loop) starting, refresh rate %v\n", currentRefreshRate)

	var err error

	ticker := time.NewTicker(currentRefreshRate)

	var channelsDataMap *map[string]*[16]util.CRSFValue
	var channelsData *[16]util.CRSFValue

	// W17 fork addition: tracks whether channel frames are currently suppressed,
	// so the transitions are logged once each instead of at the refresh rate.
	suppressed := false

	c.sentPacketsCount = 0

Loop:
	for {
		select {
		case <-c.sendLoopTomb.Dying():
			break Loop
		case chData := <-sendChan:
			//fmt.Printf("chData: %v\n", chData)
			switch data := (chData).(type) {
			//receive data from the recv loop
			case ChannelRequest:
				if data == SendModelId {
					fmt.Printf("(send-loop) writing model id frame\n")
					if _, err = port.Write(crsf.CreateModelIDFrame(0)); err != nil {
						c.errorPacketsCount += 1
						fmt.Printf("(send-loop) could not write model id frame on port %s. %s\n", port.Name, err.Error())
						// W17 fork modification: tear the loop down so the
						// supervisor reconnects, as the channels write already
						// does. Upstream only counted the error, which was
						// harmless while channel writes ran every tick and
						// caught a dead port first. With channel frames
						// suppressed under no-config, this periodic write is
						// the only thing left that can notice the port died.
						break Loop
					}
					continue
				} else if data == PingDevices {
					fmt.Printf("(send-loop) pinging devices\n")
					if _, err = port.Write(crsf.CreatePingDevicesFrame()); err != nil {
						c.errorPacketsCount += 1
						fmt.Printf("(send-loop) could not write ping devices frame on port %s. %s\n", port.Name, err.Error())
						break
					}
				}
			case *ReadDeviceFieldsRequest:
				//fmt.Printf("(send-loop) reading device fields (deviceId: %v)\n", data.deviceId)
				if _, err = port.Write(crsf.CreateParameterSettingsReadFrame(data.deviceId, data.fieldId, data.fieldChunk)); err != nil {
					c.errorPacketsCount += 1
					fmt.Printf("(send-loop) could not write \"parameters-settings-read\" frame on port %s. %s\n", port.Name, err.Error())
					break
				}
			case *WriteDeviceFieldRequestUint8:
				fmt.Printf("(send-loop) setting device field (deviceId: %v, fieldId: %v, value(uint8): %v)\n", data.deviceId, data.fieldId, data.fieldValue)
				if _, err = port.Write(crsf.CreateParameterSettingWriteFrameUint8(data.deviceId, data.fieldId, data.fieldValue)); err != nil {
					c.errorPacketsCount += 1
					fmt.Printf("(send-loop) could not write \"parameters-settings-write-uint8\" frame on port %s. %s\n", port.Name, err.Error())
					break
				}
			case *WriteDeviceFieldRequestUint16:
				fmt.Printf("(send-loop) setting device field (deviceId: %v, fieldId: %v, value(uint16): %v)\n", data.deviceId, data.fieldId, data.fieldValue)
				if _, err = port.Write(crsf.CreateParameterSettingWriteFrameUint16(data.deviceId, data.fieldId, data.fieldValue)); err != nil {
					c.errorPacketsCount += 1
					fmt.Printf("(send-loop) could not write \"parameters-settings-write-uint16\" frame on port %s. %s\n", port.Name, err.Error())
					break
				}

			case *telem.TelemSyncType:
				nextRefreshRate = crsf.AdjustSendRate((*data).Rate(), (*data).Offset())
				ticker.Reset(nextRefreshRate)
				//fmt.Printf("(send-loop) rate: %v, offset: %v, newRate: %v\n", time.Duration((*data).Rate()/10)*time.Microsecond, time.Duration((*data).Offset()/10)*time.Microsecond, nextRefreshRate)
			default:
				fmt.Printf("(send-loop) unknown channel request\n")
			}

		case <-ticker.C:
			if channelsDataMap != c.configCtl.EvalDataMap && c.configCtl.EvalDataMap != nil {
				channelsDataMap = c.configCtl.EvalDataMap
			}

			channelsData = resolveChannels(channelsDataMap, port.Name)

			// W17 fork modification -- failsafe gap. When no config resolves for
			// this port, send NO channel frame at all rather than a synthesized
			// one. See resolveChannels for why any invented payload is unsafe.
			if channelsData == nil {
				if !suppressed {
					suppressed = true
					fmt.Printf("(send-loop) no config for port %s: suppressing channel frames "+
						"so the receiver's link-loss failsafe can fire\n", port.Name)
				}
				continue
			}

			if suppressed {
				suppressed = false
				fmt.Printf("(send-loop) config resolved for port %s: resuming channel frames\n", port.Name)
			}

			if _, err = port.Write(crsf.PackChannels(channelsData)); err != nil {
				fmt.Printf("(send-loop) could not write channels on port %s. %s\n", port.Name, err.Error())
				break Loop
			}

			//if currentRefreshRate != nextRefreshRate {
			//	fmt.Printf("(send-loop) oldRate: %v, newRate: %v, packets: %v\n", currentRefreshRate, nextRefreshRate, c.sentPacketsCount)
			//	currentRefreshRate = nextRefreshRate
			//	ticker.Reset(nextRefreshRate)
			//}
			c.sentPacketsCount += 1
		}
	}

	fmt.Println("(send-loop): exiting send loop ...")

	return nil
}

//goland:noinspection GoUnusedFunction
func printChannels(channels *[16]util.CRSFValue) {
	fmt.Printf("(send-loop) r: %04d, p: %04d, t: %04d, y: %04d, arm: %04d, pre: %04d, mode: %04d\n", channels[0], channels[1], channels[2], channels[3], channels[4], channels[5], channels[6])
}
