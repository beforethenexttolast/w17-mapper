// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package link

// Regression tests for the config-swap failsafe gap in SendLoop.
//
// The defect: applying a new config mid-session does not update the existing
// transmitter arrays, it REPLACES them. config.GetTransmitters builds a fresh
// OutputTransmitter per port through NewTransmitter, whose array starts at
// centeredValues() -- all 16 slots at 992 -- and only the channel-node lists
// carry across. A channel the new config no longer maps therefore drops from
// whatever it was holding to 992 and stays there permanently, because nothing
// re-evaluates a slot that no node writes.
//
// 992 is not neutral for a switch. It normalizes to 0, which sits inside the
// firmware's +/-250 hysteresis dead band, so decodeSwitch HOLDS the previous
// state: an arm channel that was ON before the swap stays ON, with nothing left
// driving it and no way to command it off.
//
// The earlier no-config fix (630ea96) covers the CLEARED-config case by
// suppressing frames entirely. The REPLACED-config case is not covered by it --
// resolveChannels gets a live map hit and hands back the all-992 array.
//
// The fix reuses that same shape: emit no channel frame at all across the swap.
// During a swap the mapper genuinely does not know what the channel values
// should be, and every alternative keeps transmitting a guess -- carrying the
// previous values forward IS hold-last, the exact semantic the config-layer fix
// was written to remove. Sending nothing routes the swap into the receiver's own
// link-loss failsafe, which is the designed response to "no valid control input"
// and, unlike any in-band payload, actually clears a latched switch.

import (
	"testing"
	"time"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// firmwareLinkTimeout is failsafe::Config::linkTimeoutMs in w17-control-fw: no
// valid CRSF frame for this long forces the Safe state. The suppression window
// is useless if it is shorter -- the whole point is to let that timeout expire.
const firmwareLinkTimeout = 500 * time.Millisecond

// armedValues stands in for the wire state at the moment of a swap: an arm
// channel latched hard ON.
func armedValues() *[16]util.CRSFValue {
	values := centeredTestValues()
	values[4] = util.CRSFValue(1811)
	return values
}

// TestSwapSuppressesEvenThoughTheNewConfigResolves is the defect itself. The new
// config resolves for this port -- resolveChannels hands back a real array, so
// the no-config suppression does not fire -- and that array reads 992 on the arm
// channel the new config dropped. Transmitting it leaves the switch latched.
func TestSwapSuppressesEvenThoughTheNewConfigResolves(t *testing.T) {
	now := time.Now()

	gate := &configSwapGate{}
	gate.observe(&map[string]*[16]util.CRSFValue{testPort: armedValues()}, now)

	// A new config is applied. It no longer maps ch5, so the rebuilt array
	// carries center there -- which a receiver HOLDS.
	swapped := &map[string]*[16]util.CRSFValue{testPort: centeredTestValues()}
	now = now.Add(time.Second)
	gate.observe(swapped, now)

	if values := gate.values(testPort); values == nil {
		t.Fatalf("setup: the replaced config resolves for this port, unlike a cleared one")
	} else if values[4] != util.CRSFValue(util.CRSFCenterValue) {
		t.Fatalf("setup: expected the dropped channel to read center, got %d", values[4])
	}

	if !gate.holdOff(now) {
		t.Errorf("frames must be suppressed across the swap: transmitting center on a " +
			"dropped switch channel leaves it latched in the receiver's hysteresis band")
	}
}

// TestWindowOutlastsTheReceiversFailsafeTimeout is the invariant that makes
// suppression mean anything. A window shorter than the downstream timeout
// changes nothing on the wire -- the receiver never notices the gap, never
// fails safe, and the latched switch survives exactly as before.
func TestWindowOutlastsTheReceiversFailsafeTimeout(t *testing.T) {
	if configSwapFailsafeWindow <= firmwareLinkTimeout {
		t.Errorf("the swap window (%v) must outlast the firmware's link timeout (%v), "+
			"or no failsafe fires and suppressing accomplishes nothing",
			configSwapFailsafeWindow, firmwareLinkTimeout)
	}
}

// TestFirstPublishOpensAWindow covers link bring-up: the send loop starts before
// any config exists, so the first published map is itself a transition into
// transmitting and gets the same treatment.
func TestFirstPublishOpensAWindow(t *testing.T) {
	now := time.Now()
	gate := &configSwapGate{}

	if gate.holdOff(now) {
		t.Errorf("a gate that has adopted nothing has no window to hold off on")
	}

	if opened := gate.observe(&map[string]*[16]util.CRSFValue{testPort: centeredTestValues()}, now); !opened {
		t.Fatalf("expected the first publish to open a window")
	}
	if !gate.holdOff(now) {
		t.Errorf("expected frames to be suppressed inside the window")
	}
}

// TestSteadyStateDoesNotReopenTheWindow is the over-correction guard. The eval
// loop publishes a new map only when a config is applied; it mutates the arrays
// in place on every other tick. Re-observing the same map must not restart
// suppression, or a live link would never transmit at all.
func TestSteadyStateDoesNotReopenTheWindow(t *testing.T) {
	now := time.Now()
	published := &map[string]*[16]util.CRSFValue{testPort: centeredTestValues()}

	gate := &configSwapGate{}
	gate.observe(published, now)

	now = now.Add(configSwapFailsafeWindow)

	for tick := 0; tick < 100; tick++ {
		if opened := gate.observe(published, now); opened {
			t.Fatalf("tick %d: an unchanged map reopened the window", tick)
		}
		if gate.holdOff(now) {
			t.Fatalf("tick %d: frames still suppressed after the window elapsed", tick)
		}
		now = now.Add(4 * time.Millisecond) // roughly one CRSF refresh
	}

	if gate.values(testPort) == nil {
		t.Errorf("a live config must transmit once its window has passed")
	}
}

// TestEachSwapReopensTheWindow covers the activity that actually triggers this:
// hand-entering a config means pressing Apply repeatedly, and every press is a
// swap.
func TestEachSwapReopensTheWindow(t *testing.T) {
	now := time.Now()
	gate := &configSwapGate{}

	for apply := 0; apply < 3; apply++ {
		published := &map[string]*[16]util.CRSFValue{testPort: centeredTestValues()}

		if opened := gate.observe(published, now); !opened {
			t.Fatalf("apply %d: expected a fresh map to open a window", apply)
		}

		now = now.Add(configSwapFailsafeWindow - time.Millisecond)
		if !gate.holdOff(now) {
			t.Errorf("apply %d: window closed early", apply)
		}

		now = now.Add(2 * time.Millisecond)
		if gate.holdOff(now) {
			t.Errorf("apply %d: window did not close", apply)
		}
	}
}

// TestNoConfigSuppressionStaysUnbounded pins that the window is a MINIMUM, never
// a timeout. If a config never resolves for this port, suppression is permanent
// -- that is the safe outcome, and a bound would put the mapper back to
// transmitting a payload it has no basis for.
func TestNoConfigSuppressionStaysUnbounded(t *testing.T) {
	now := time.Now()

	gate := &configSwapGate{}
	gate.observe(&map[string]*[16]util.CRSFValue{"/dev/ttyOTHER": centeredTestValues()}, now)

	for _, elapsed := range []time.Duration{
		configSwapFailsafeWindow,
		time.Minute,
		time.Hour,
	} {
		if got := gate.values(testPort); got != nil {
			t.Errorf("after %v a port with no config must still yield no frame, got %v",
				elapsed, *got)
		}
	}
}

// TestNilPublishLeavesTheGateAlone covers the send loop starting before the
// config controller has published anything: a nil map must not be adopted over a
// live one, which is the behaviour the loop had before the gate existed.
func TestNilPublishLeavesTheGateAlone(t *testing.T) {
	now := time.Now()
	published := &map[string]*[16]util.CRSFValue{testPort: centeredTestValues()}

	gate := &configSwapGate{}
	gate.observe(published, now)
	now = now.Add(configSwapFailsafeWindow)

	if opened := gate.observe(nil, now); opened {
		t.Errorf("a nil map must not open a window")
	}
	if gate.values(testPort) == nil {
		t.Errorf("a nil map must not displace the live one")
	}
}
