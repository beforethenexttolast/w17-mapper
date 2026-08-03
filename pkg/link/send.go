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
	"sync/atomic"
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

// portUnresolved reports whether the config layer has told the send loop that it
// cannot account for this port's channels, and that nothing may therefore be
// transmitted for it. W17 fork addition.
//
// The condition behind it: OutputTransmitter.Eval walks a top-level entry's
// subtree to find the channel nodes it drives, and that walk is depth-bounded.
// If the bound truncates the walk, the owner set is incomplete -- some channel
// may be left holding a stale value with nothing able to neutralize it, because
// the code no longer knows the channel exists. That is unknown state, and the
// answer to unknown state on this path is the one resolveChannels and
// configSwapGate already give: send nothing and let the receiver's own link-loss
// failsafe resolve it.
//
// A nil map (no config applied yet) is not unresolved; that case is already
// covered by resolveChannels finding no entry for the port.
func portUnresolved(unresolvedMap *map[string]*atomic.Bool, portName string) bool {
	if unresolvedMap == nil {
		return false
	}

	flag, ok := (*unresolvedMap)[portName]
	if !ok || flag == nil {
		return false
	}

	return flag.Load()
}

// suppressionReason reports why this tick must transmit no channel frame, or ""
// when it may transmit. W17 fork addition.
//
// Three independent causes now share one outcome, so the composition is worth
// stating rather than leaving inline: ANY of them suppresses, and none of them
// can end a suppression another is holding open. That is the property that keeps
// the swap window a minimum and never a timeout -- a window expiring while the
// config still does not resolve, or while the config layer still cannot account
// for its channels, changes nothing. The reason string is for the transition log
// only; the first listed cause wins when several hold at once, and which one is
// reported has no effect on behaviour.
func suppressionReason(channelsData *[16]util.CRSFValue, swapHoldOff bool, unresolved bool) string {
	switch {
	case channelsData == nil:
		return "no config resolves for this port"
	case swapHoldOff:
		return "a config was just applied"
	case unresolved:
		return "the config layer cannot account for every channel it drives"
	}

	return ""
}

// configSwapFailsafeWindow is how long channel frames stay suppressed after the
// mapper publishes a replacement config. W17 fork addition.
//
// It is a MINIMUM, not a timeout: it lengthens the no-frame window, it never
// ends one (see configSwapGate.holdOff). A window is only useful if it is long
// enough for the downstream link-loss failsafe to actually fire, which is the
// entire point of suppressing -- a swap that resumed in microseconds would
// leave a dropped switch channel latched exactly as before.
//
// Sizing: the W17 control firmware forces its Safe state after 500 ms with no
// valid CRSF frame (failsafe::Config::linkTimeoutMs). 1 s gives that a 2x
// margin and some room for the TX-module/RX hold on top, which has NOT been
// measured -- no hardware has run this path. Treat the exact value as a bench
// item; the invariant to preserve is "comfortably longer than the receiving
// end's failsafe timeout".
const configSwapFailsafeWindow = 1000 * time.Millisecond

// configSwapGate holds channel frames off across a config swap. W17 fork
// addition.
//
// The defect it closes: applying a new config rebuilds the transmitter arrays
// from scratch (config.NewTransmitter -> centeredValues), so a channel the new
// config no longer maps drops from whatever it held to 992 and stays there --
// nothing re-evaluates a slot no node writes. 992 normalizes to 0, which sits
// inside the firmware's +/-250 hysteresis dead band, so a switch decoder HOLDS
// its previous state: an arm channel that was ON before the swap stays ON, with
// nothing left driving it. The mapper has no basis for inventing a value for a
// channel it no longer maps, so it emits nothing at all for a window and lets
// the receiver's own link-loss failsafe resolve the state -- the same reasoning,
// and the same shape, as the no-config suppression in resolveChannels.
//
// Clock-free by construction: the caller supplies now, so this is testable
// without sleeping and the send loop keeps a single time source per tick.
type configSwapGate struct {
	current *map[string]*[16]util.CRSFValue
	adopted bool
	until   time.Time
}

// observe adopts a newly published eval map and opens a suppression window when
// the map it is transmitting from has been replaced. Reports whether this call
// opened one. A nil map is ignored rather than adopted, so a config controller
// that has not published yet leaves the previous map in place.
func (g *configSwapGate) observe(published *map[string]*[16]util.CRSFValue, now time.Time) bool {
	if published == nil {
		return false
	}
	if g.adopted && g.current == published {
		return false
	}

	g.current = published
	g.adopted = true
	g.until = now.Add(configSwapFailsafeWindow)

	return true
}

// holdOff reports whether the swap window is still open.
func (g *configSwapGate) holdOff(now time.Time) bool {
	return g.adopted && now.Before(g.until)
}

// values returns the array the gate is currently transmitting from.
func (g *configSwapGate) values(portName string) *[16]util.CRSFValue {
	return resolveChannels(g.current, portName)
}

//goland:noinspection GoUnusedParameter
func (c *Controller) SendLoop(port *serial.Port, sendChan chan any, recvChan chan any) error {

	currentRefreshRate := crsf.GetRefreshRate(port.BaudRate)
	nextRefreshRate := currentRefreshRate

	fmt.Printf("(send-loop) starting, refresh rate %v\n", currentRefreshRate)

	var err error

	ticker := time.NewTicker(currentRefreshRate)

	var channelsData *[16]util.CRSFValue

	// W17 fork addition: tracks whether channel frames are currently suppressed,
	// so the transitions are logged once each instead of at the refresh rate.
	suppressed := false

	// W17 fork addition: holds frames off across a config swap. See configSwapGate.
	gate := &configSwapGate{}

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
			now := time.Now()

			if gate.observe(c.configCtl.EvalDataMap, now) {
				fmt.Printf("(send-loop) config published for port %s: suppressing channel frames "+
					"for %v so the receiver's link-loss failsafe can fire across the swap\n",
					port.Name, configSwapFailsafeWindow)
			}

			channelsData = gate.values(port.Name)

			// W17 fork modification -- failsafe gap. When no config resolves for
			// this port, send NO channel frame at all rather than a synthesized
			// one. See resolveChannels for why any invented payload is unsafe.
			//
			// The swap window is the same suppression for the same reason: a
			// replacement config re-seeds every channel it does not map to 992,
			// and 992 is HELD by a receiver's switch hysteresis. See
			// configSwapGate.
			//
			// The third cause is a truncated owner walk: the config layer cannot
			// account for every channel it drives. See portUnresolved.
			reason := suppressionReason(
				channelsData,
				gate.holdOff(now),
				portUnresolved(c.configCtl.EvalUnresolvedMap, port.Name),
			)

			if reason != "" {
				if !suppressed {
					suppressed = true
					fmt.Printf("(send-loop) suppressing channel frames on port %s (%s) "+
						"so the receiver's link-loss failsafe can fire\n", port.Name, reason)
				}
				continue
			}

			if suppressed {
				suppressed = false
				fmt.Printf("(send-loop) resuming channel frames on port %s\n", port.Name)
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
