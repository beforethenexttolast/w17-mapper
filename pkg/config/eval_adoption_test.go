// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for the SetConfig adoption handshake (MAP-4).
//
// The defect: SetConfig set c.Config, fired a NON-BLOCKING send on an
// UNBUFFERED channel, and returned success. When the eval loop was not sitting
// at its select in that instant the alert was dropped -- and nothing ever
// noticed. c.Config was the new config, so GetConfig and the editor showed it
// as applied, while the eval loop's published arrays -- the ones the send loop
// actually transmits -- still belonged to the previous config, permanently:
// the 25 ms heartbeat re-evaluates the holders it already has, so it repairs a
// dropped DEVICE alert but can never repair a dropped CONFIG alert.
//
// On the race-day path SetConfig is the FIRST thing that happens after launch,
// so the failure mode was "the profile loaded" with the profile not loaded.
//
// The fix under test: ConfigEventChan is buffered, delivery is a blocking send
// with an escape, and SetConfig returns only once the eval loop has published
// the new arrays.

import (
	"testing"
	"time"

	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const adoptionTestPort = "/dev/adoption-test"

func adoptionTestConfig(ctl *Controller, value util.RawValue) *Config {
	cfg := &Config{IOMap: map[string]*IOHolder{}}
	cfg.Ctl = ctl

	list := []*IOHolder{numberChannel(1, value)}
	cfg.IOMap["tx"] = &IOHolder{
		IO: &OutputTransmitter{
			Id: "tx", Type: "tx",
			Transmitter: TransmitterT{Port: adoptionTestPort, Channels: &list},
		},
		Ctl: ctl, Config: cfg,
	}
	return cfg
}

// TestSetConfigReturnsOnlyAfterAdoption is the MAP-4 gate. The assertion is
// made the instant SetConfig returns, with no sleep and no retry: the config
// the caller was told was applied must already be the one the send loop reads.
func TestSetConfigReturnsOnlyAfterAdoption(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	ctl.SetConfig(adoptionTestConfig(ctl, util.MaxRaw))

	if ctl.EvalDataMap == nil {
		t.Fatal("SetConfig returned before anything was published")
	}
	values, ok := (*ctl.EvalDataMap)[adoptionTestPort]
	if !ok {
		t.Fatalf("SetConfig returned before the port was published; have %v", *ctl.EvalDataMap)
	}
	if (*values)[0] != full {
		t.Errorf("ch1 = %d, want %d -- published but not evaluated", (*values)[0], full)
	}
}

// TestSetConfigAdoptsEverySuccessiveConfig covers the swap: a second Apply must
// also be waited for, not just the first. A barrier that fired once would make
// every later SetConfig return immediately -- exactly the bug, one release
// later.
func TestSetConfigAdoptsEverySuccessiveConfig(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	for i, want := range []util.RawValue{util.MaxRaw, util.MinRaw, util.MaxRaw} {
		ctl.SetConfig(adoptionTestConfig(ctl, want))

		values, ok := (*ctl.EvalDataMap)[adoptionTestPort]
		if !ok {
			t.Fatalf("apply %d: the port was not published when SetConfig returned", i)
		}
		if got := (*values)[0]; got != util.CRSFValue(scaleForTest(want)) {
			t.Errorf("apply %d: ch1 = %d, want %d", i, got, scaleForTest(want))
		}
	}
}

// TestConfigEventChanIsBuffered pins the defence in depth behind the handshake:
// even if a future caller goes back to a non-blocking send, one config can
// always be parked rather than dropped.
func TestConfigEventChanIsBuffered(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	if cap(ctl.ConfigEventChan) < 1 {
		t.Errorf("ConfigEventChan capacity = %d, want at least 1", cap(ctl.ConfigEventChan))
	}
}

// TestSetConfigWithoutAnEvalLoopDoesNotBlock protects every load-path test and
// every shutdown race: a controller with no running loop has nothing that can
// adopt a config, so SetConfig must set the field and return rather than wait
// out its timeout.
func TestSetConfigWithoutAnEvalLoopDoesNotBlock(t *testing.T) {
	bare := &Controller{}

	done := make(chan struct{})
	go func() {
		bare.SetConfig(&Config{IOMap: map[string]*IOHolder{}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig blocked on a controller with no eval loop")
	}

	if bare.Config == nil {
		t.Error("the config should still have been set")
	}
}

// TestSetConfigAfterQuitDoesNotBlock is the same requirement on the shutdown
// side: the loop is gone, so there is nothing to wait for.
func TestSetConfigAfterQuitDoesNotBlock(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	ctl.Quit()

	done := make(chan struct{})
	go func() {
		ctl.SetConfig(adoptionTestConfig(ctl, util.MaxRaw))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig blocked after the eval loop was stopped")
	}
}

// scaleForTest mirrors what a number channel publishes for a raw value: the
// endpoints map to the CRSF anchors.
func scaleForTest(raw util.RawValue) util.CRSFValue {
	if raw == util.MaxRaw {
		return full
	}
	return util.CRSFValue(util.CRSFMinValue)
}
