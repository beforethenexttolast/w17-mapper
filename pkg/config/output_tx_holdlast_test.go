// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression tests for the hold-last failsafe gap in OutputTransmitter.Eval.
//
// The defect: an input node that could not resolve its gamepad returned nan=true,
// and Eval reacted with `continue`. Because Values is a persistent array mutated
// in place and never reset, the affected channel kept the previous tick's value
// forever. The mapper went on transmitting well-formed CRSF at full rate with a
// stale payload, so the receiver saw a healthy link and no link-loss failsafe
// could fire — a gamepad dropout froze throttle and steering wherever they were.
//
// The fix has two halves, both covered here:
//   1. A nan channel is driven to its configured failsafe value (center by
//      default, OFF rail for switch-like channels) instead of being skipped.
//   2. A detached device no longer resolves, so a hot-unplug reaches that nan
//      path at all. The registry is never pruned on removal, so before the fix
//      an unplugged device still resolved and the nodes read stale axis values
//      from a detached SDL handle — never reaching nan.

import (
	"testing"

	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const center = util.CRSFValue(util.CRSFCenterValue)

// offRail is the CRSF value a switch-like channel should fall to: the bottom of
// the nominal range, comfortably outside any receiver's hysteresis band.
const offRail = util.RawValue(172)

// newTestConfig builds a Config whose device registry holds an entry for each id
// given. Those entries have a nil SDL handle, which InputGamepad.Attached()
// reports as detached — exactly the state of an unplugged device, and the reason
// no test here needs real hardware. Ids are present in the map but unresolvable,
// which is precisely the case the registry never prunes.
func newTestConfig(detachedIds ...string) *Config {
	gamepads := map[string]*dc.InputGamepad{}
	for _, id := range detachedIds {
		gamepads[id] = &dc.InputGamepad{Id: id, Name: "detached-" + id}
	}

	cfg := &Config{IOMap: map[string]*IOHolder{}}
	cfg.Ctl = &Controller{
		deviceCtl:  &dc.Controller{Gamepads: gamepads},
		EvalNoData: &[16]util.CRSFValue{},
	}
	return cfg
}

// axisChannel wires: InputChannel(number) <- InputAxis <- InputGamepad(deviceId).
// This is the shape the real UI produces for a throttle or steering channel.
// A nil failsafe exercises the center default.
func axisChannel(number int32, deviceId string, failsafe *util.RawValue) *IOHolder {
	deadzone := util.ZeroRaw

	gamepad := &IOHolder{IO: &InputGamepad{
		Id:      "gp-node",
		Type:    "gamepad",
		Gamepad: GamepadT{Id: deviceId, Name: "test"},
	}}

	axis := &IOHolder{IO: &InputAxis{
		Id:   "axis-node",
		Type: "axis",
		Axis: AxisT{Input: gamepad, Number: 0, Deadzone: &deadzone},
	}}

	return channelNode(number, axis, failsafe)
}

// numberChannel wires InputChannel(number) <- InputNumber(raw). It stands in for
// a healthy tick, so no test needs a real SDL joystick to produce a live value.
func numberChannel(number int32, raw util.RawValue) *IOHolder {
	num := &IOHolder{IO: &InputNumber{
		Id: "num-node", Type: "number", Number: NumberT{Output: raw},
	}}
	return channelNode(number, num, nil)
}

func channelNode(number int32, input *IOHolder, failsafe *util.RawValue) *IOHolder {
	crsfMin, crsfMax := util.RawValue(util.CRSFMinValue), util.RawValue(util.CRSFMaxValue)
	rawMin, rawMax := util.MinRaw, util.MaxRaw

	return &IOHolder{IO: &InputChannel{
		Id:   "ch-node",
		Type: "channel",
		Channel: ChannelT{
			Number: number, Input: input,
			CRSFMin: &crsfMin, CRSFMax: &crsfMax,
			RawMin: &rawMin, RawMax: &rawMax,
			Failsafe: failsafe,
		},
	}}
}

func newTx(channels ...*IOHolder) *OutputTransmitter {
	list := channels
	return &OutputTransmitter{
		Id:   "tx",
		Type: "transmitter",
		Transmitter: TransmitterT{
			Port:     "/dev/test",
			Channels: &list,
		},
	}
}

// TestDetachedDeviceDoesNotResolve covers the second half of the fix. The id is
// present in the registry — removal never prunes it — but the handle is detached,
// so resolution must fail rather than hand back a gamepad whose axis reads are
// frozen garbage.
func TestDetachedDeviceDoesNotResolve(t *testing.T) {
	cfg := newTestConfig("unplugged")

	if _, ok := cfg.Ctl.deviceCtl.Gamepads["unplugged"]; !ok {
		t.Fatalf("setup: the registry should still hold the entry (it is never pruned)")
	}

	if gp, ok := cfg.GetInputGamepad("unplugged"); ok {
		t.Errorf("a detached device must not resolve, got %+v", gp)
	}
}

// TestDeviceMissYieldsNaN covers step 1 of the original chain: an unresolvable
// device makes the axis -> channel subtree evaluate to nan, while still reporting
// its channel number so the channel can be neutralized rather than skipped.
func TestDeviceMissYieldsNaN(t *testing.T) {
	cfg := newTestConfig() // empty registry

	_, _, chNum, nan := axisChannel(1, "absent-device", nil).Eval(cfg)

	if !nan {
		t.Fatalf("expected nan=true for an unresolvable device, got nan=false")
	}
	if chNum != 1 {
		t.Fatalf("expected channel number 1 to survive the nan, got %d "+
			"(without it the assembler cannot neutralize the channel)", chNum)
	}
}

// TestStaleProportionalChannelGoesToCenter is the fix for the reported defect: a
// channel that produced full deflection must fall to center, not hold, once its
// device stops resolving.
func TestStaleProportionalChannelGoesToCenter(t *testing.T) {
	cfg := newTestConfig("unplugged")

	// Tick 1: healthy, full-deflection throttle.
	tx := newTx(numberChannel(1, util.MaxRaw))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != util.CRSFValue(util.CRSFMaxValue) {
		t.Fatalf("setup: expected ch1=%d after a healthy tick, got %d",
			util.CRSFMaxValue, got)
	}

	// Tick 2+: the gamepad is unplugged mid-drive.
	*tx.Transmitter.Channels = []*IOHolder{axisChannel(1, "unplugged", nil)}

	for tick := 0; tick < 5; tick++ {
		tx.Eval(cfg)
		if got := (*tx.Values)[0]; got != center {
			t.Fatalf("tick %d: expected ch1 to hold center %d, got %d",
				tick, center, got)
		}
	}
}

// TestStaleSwitchChannelGoesToOffRail covers why the neutral is per-channel. A
// receiver applying hysteresis around center HOLDS a switch's previous state at
// 992, so an arm channel must fall to its OFF rail to actually disarm.
func TestStaleSwitchChannelGoesToOffRail(t *testing.T) {
	cfg := newTestConfig("unplugged")

	// Tick 1: arm channel driven high.
	tx := newTx(numberChannel(5, util.MaxRaw))
	tx.Eval(cfg)

	if got := (*tx.Values)[4]; got != util.CRSFValue(util.CRSFMaxValue) {
		t.Fatalf("setup: expected ch5 high, got %d", got)
	}

	rail := offRail
	*tx.Transmitter.Channels = []*IOHolder{axisChannel(5, "unplugged", &rail)}
	tx.Eval(cfg)

	if got := (*tx.Values)[4]; got != util.CRSFValue(offRail) {
		t.Errorf("expected the arm channel to fall to its OFF rail %d, got %d",
			offRail, got)
	}
	if got := (*tx.Values)[4]; got == center {
		t.Errorf("a switch channel at center would stay latched in a receiver's " +
			"hysteresis band")
	}
}

// TestNeutralSurvivesDeviceEventAlert answers the question the original report
// left open: does device-event handling zero or invalidate the array before the
// next tick? It does not — AlertDeviceChan only bumps a counter and pokes a
// channel, and the EvalLoop consumer re-runs Eval on the SAME array. The neutral
// therefore has to come from Eval itself, which is what this asserts.
func TestNeutralSurvivesDeviceEventAlert(t *testing.T) {
	cfg := newTestConfig("unplugged")

	tx := newTx(numberChannel(2, util.MaxRaw))
	tx.Eval(cfg)
	valuesPtrBefore := tx.Values

	devCtl := cfg.Ctl.deviceCtl
	devCtl.DeviceEventChan = make(chan int32, 1)
	devCtl.AlertDeviceChan()

	if len(devCtl.DeviceEventChan) != 1 {
		t.Fatalf("expected the device alert to be queued")
	}
	<-devCtl.DeviceEventChan // what EvalLoop's consumer does

	*tx.Transmitter.Channels = []*IOHolder{axisChannel(2, "unplugged", nil)}
	tx.Eval(cfg)

	if tx.Values != valuesPtrBefore {
		t.Errorf("Values was reallocated; the in-place-mutation premise no longer holds")
	}
	if got := (*tx.Values)[1]; got != center {
		t.Errorf("expected ch2 at center %d after the device event, got %d", center, got)
	}
}

// TestUnmappedChannelsStartCentered guards the initial state. A zero-valued array
// is not neutral: 0 is below the nominal [172, 1811] CRSF range, so a receiver
// normalizing against those anchors reads it as FULL NEGATIVE deflection. Every
// channel a config does not map would otherwise command hard-over output.
func TestUnmappedChannelsStartCentered(t *testing.T) {
	for _, tx := range []*OutputTransmitter{
		NewTransmitter("/dev/test"),
		newTx(numberChannel(1, util.MaxRaw)), // Values allocated lazily by Eval
	} {
		if tx.Values == nil {
			tx.Eval(newTestConfig())
		}
		for ch, got := range *tx.Values {
			if ch == 0 {
				continue // may carry the mapped value
			}
			if got != center {
				t.Errorf("unmapped ch%d starts at %d, expected center %d",
					ch+1, got, center)
			}
		}
	}
}

// TestRecoveryAfterReattach checks the neutral is not latched: once the input
// resolves again the channel must track it, otherwise the fix would trade a
// frozen stick for a dead one.
func TestRecoveryAfterReattach(t *testing.T) {
	cfg := newTestConfig("unplugged")

	tx := newTx(axisChannel(1, "unplugged", nil))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != center {
		t.Fatalf("setup: expected center while detached, got %d", got)
	}

	*tx.Transmitter.Channels = []*IOHolder{numberChannel(1, util.MaxRaw)}
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != util.CRSFValue(util.CRSFMaxValue) {
		t.Errorf("expected the channel to track live input again (%d), got %d",
			util.CRSFMaxValue, got)
	}
}

// TestFailsafeDefaultIsCenterNotZero pins the default for configs built in Go,
// which never run UnmarshalJSON, and states the invariant that keeps 0 out of the
// failsafe path.
func TestFailsafeDefaultIsCenterNotZero(t *testing.T) {
	ch := &InputChannel{Channel: ChannelT{Number: 1}} // Failsafe nil

	if got := ch.FailsafeValue(); got != center {
		t.Errorf("expected the nil-failsafe default to be center %d, got %d", center, got)
	}

	if util.CRSFCenterValue == util.CRSFMinValue {
		t.Errorf("center must not equal the CRSF minimum: 0 decodes as full " +
			"negative deflection, not as neutral")
	}
}
