// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression test for the droppable neutralization tick (2026-08-16 audit,
// defect 2 / RESIDUAL C).
//
// The defect: every path that re-evaluated a transmitter was event-driven.
// AlertDeviceChan is a non-blocking send on an unbuffered channel with
// competing receivers, and the 25 ms tickers that force evaluation all live
// inside streaming RPC handlers. With ZERO gRPC subscribers, a device removal
// whose one alert burst was dropped left the pre-removal values transmitting
// at the full CRSF rate indefinitely -- the per-channel failsafes existed but
// nothing ever ran Eval to apply them.
//
// The fix under test: the eval loop's own heartbeat re-evaluates every
// synthetic transmitter at least once per evalHeartbeatInterval, uncondition-
// ally. The scenario below runs a REAL controller with a REAL eval loop and
// NO subscribers, kills the input silently (no alert of any kind -- exactly a
// dropped removal alert), and requires the published array to fall to the
// channel's failsafe.
//
// Synchronization notes, because the loop owns everything it evaluates:
//   - the config is delivered with a BLOCKING send on ConfigEventChan, so
//     delivery cannot be dropped;
//   - the input dies through an atomic flag, giving the loop a race-free view;
//   - progress is observed by receiving from EvalEventChan (each successful
//     receive is a rendezvous with an alert sent right after a transmitter
//     eval, so two receives after the flag flips prove a full eval pass ran
//     with the flag visible);
//   - the published values are read only AFTER Quit joins the loop goroutine.

import (
	"sync/atomic"
	"testing"
	"time"

	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const heartbeatTestPort = "/dev/heartbeat-test"

// dyingInput resolves to full deflection until dead is set, then to nan --
// the same observable behaviour as an axis on a gamepad that was unplugged,
// minus the SDL handle a test cannot fabricate.
type dyingInput struct {
	dead *atomic.Bool
}

func (f *dyingInput) Eval(*Config) (IOType, util.RawValue, util.ChannelNumber, bool) {
	if f.dead.Load() {
		return nil, 0, -1, true
	}
	return nil, util.MaxRaw, -1, false
}
func (f *dyingInput) InputType() string          { return "test-dying" }
func (f *dyingInput) InputValue() *util.RawValue { return nil }
func (f *dyingInput) InputId() string            { return "dying-input" }
func (f *dyingInput) Children() *[]*IOHolder     { return nil }

// waitAlert receives one eval alert or fails the test. Alerts are lossy for
// the SENDER, but the heartbeat keeps producing them while a config is live,
// so a bounded blocking receive is reliable for the RECEIVER.
func waitAlert(t *testing.T, c *Controller, what string) {
	t.Helper()
	select {
	case <-c.EvalEventChan:
	case <-time.After(2 * time.Second):
		t.Fatalf("no eval alert within 2s while waiting for %s -- the loop is "+
			"not evaluating on its own", what)
	}
}

func TestHeartbeatNeutralizesWithoutAnyEvent(t *testing.T) {
	// A real controller with a real eval loop. The device controller is bare:
	// its DeviceEventChan is nil, so the device-event branch can NEVER fire --
	// which is the point. No gRPC server exists, so there are no subscriber
	// tickers either. The heartbeat is the only clock in the room.
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	dead := &atomic.Bool{}
	rail := offRail

	cfg := &Config{IOMap: map[string]*IOHolder{}}
	cfg.Ctl = ctl
	list := []*IOHolder{
		channelNode(5, &IOHolder{IO: &dyingInput{dead: dead}}, &rail),
	}
	cfg.IOMap["tx"] = &IOHolder{
		IO: &OutputTransmitter{
			Id: "tx", Type: "tx",
			Transmitter: TransmitterT{Port: heartbeatTestPort, Channels: &list},
		},
		Ctl: ctl, Config: cfg,
	}

	// Blocking send: the loop has definitely adopted the config after this.
	ctl.ConfigEventChan <- cfg
	waitAlert(t, ctl, "the config to apply")

	// The input dies. Deliberately NO alert of any kind follows.
	dead.Store(true)

	// Two rendezvous after the store prove at least one complete transmitter
	// eval started after the death was visible; a third is margin. Each wait
	// is individually bounded, so a stalled loop fails fast rather than
	// hanging the suite. The real-world bound this stands in for: one
	// evalHeartbeatInterval (25 ms) to neutralize, plus one send tick to get
	// it on the wire -- 20x inside the firmware's 500 ms link timeout.
	for i := 0; i < 3; i++ {
		waitAlert(t, ctl, "a heartbeat eval after the input died")
	}

	// Join the loop; after Quit nothing concurrent touches the arrays.
	ctl.Quit()

	values, ok := (*ctl.EvalDataMap)[heartbeatTestPort]
	if !ok {
		t.Fatalf("the port was never published; have %v", *ctl.EvalDataMap)
	}
	if got := (*values)[4]; got != util.CRSFValue(offRail) {
		t.Errorf("expected the dead switch channel at its OFF rail %d, got %d -- "+
			"stale values were left transmitting with no subscriber connected",
			offRail, got)
	}
}

// TestEvalChannelsExistBeforeSubscribers pins the StartEvalLoop change the
// heartbeat test leans on: the event channels are created before the loop
// goroutine is spawned, so anything running after NewCtl returns can receive
// from them without racing the loop's startup.
func TestEvalChannelsExistBeforeSubscribers(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	if ctl.EvalEventChan == nil {
		t.Error("EvalEventChan must exist once NewCtl returns")
	}
	if ctl.StreamEventChan == nil {
		t.Error("StreamEventChan must exist once NewCtl returns")
	}
}
