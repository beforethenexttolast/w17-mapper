// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package link

// Regression tests for the no-config failsafe gap in SendLoop.
//
// The defect: when the mapper had no config for a port it transmitted
// Controller.EvalNoData -- an all-zeros channel array -- in well-formed,
// CRC-valid CRSF frames at the full refresh rate. Zero is a legal 11-bit value
// but sits below the nominal 172..1811 range, so a receiver normalizing against
// those anchors reads it as full negative deflection on all 16 channels. Worse
// than the payload itself, transmitting ANY well-formed frame keeps the link
// looking healthy, so the receiver's link-loss failsafe -- the mechanism
// actually designed for "no valid control input" -- can never fire.
//
// The fix: resolve to nil and send no channel frame at all. These tests pin the
// resolution, including the two states that are reachable in normal operation.

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const testPort = "/dev/ttyUSB0"

// TestNilMapMeansNoFrame covers link bring-up. The send loop is started by the
// supervisor as soon as the serial port opens, with no config gate at all, so
// every session passes through this state before a config is applied.
func TestNilMapMeansNoFrame(t *testing.T) {
	if got := resolveChannels(nil, testPort); got != nil {
		t.Errorf("a nil eval map must yield no frame, got %v", *got)
	}
}

// TestClearedConfigMeansNoFrame is the dangerous case and the reason the empty
// map is tested separately from the nil one. Clearing the config sets
// EvalDataMap to a new EMPTY map rather than nil, so the map-miss path -- not
// the nil path -- is what a mid-session config clear actually takes. Before the
// fix this transmitted zeros indefinitely while the car was live.
func TestClearedConfigMeansNoFrame(t *testing.T) {
	cleared := &map[string]*[16]util.CRSFValue{}

	if got := resolveChannels(cleared, testPort); got != nil {
		t.Errorf("a cleared config must yield no frame, got %v", *got)
	}
}

// TestUnknownPortMeansNoFrame covers a config that exists but maps a different
// port than the one this send loop owns.
func TestUnknownPortMeansNoFrame(t *testing.T) {
	other := &map[string]*[16]util.CRSFValue{
		"/dev/ttyOTHER": centeredTestValues(),
	}

	if got := resolveChannels(other, testPort); got != nil {
		t.Errorf("a config for another port must yield no frame, got %v", *got)
	}
}

// TestConfiguredPortTransmits is the guard against over-correcting: the fix must
// suppress frames ONLY when there is no config, never for a live one.
func TestConfiguredPortTransmits(t *testing.T) {
	values := centeredTestValues()
	configured := &map[string]*[16]util.CRSFValue{testPort: values}

	got := resolveChannels(configured, testPort)
	if got == nil {
		t.Fatalf("a configured port must transmit, got no frame")
	}
	if got != values {
		t.Errorf("expected the transmitter's own array, got a copy or substitute")
	}
}

// TestResolutionTracksLiveUpdates pins that the resolved array is the
// transmitter's live array rather than a snapshot. The eval loop mutates that
// array in place every tick, so a copy here would freeze the channels at
// whatever they held when the config was applied -- reintroducing a hold-last
// bug at a different layer.
func TestResolutionTracksLiveUpdates(t *testing.T) {
	values := centeredTestValues()
	configured := &map[string]*[16]util.CRSFValue{testPort: values}

	resolved := resolveChannels(configured, testPort)
	if resolved == nil {
		t.Fatalf("setup: expected the port to resolve")
	}

	values[0] = util.CRSFValue(1811) // what an eval tick does

	if (*resolved)[0] != util.CRSFValue(1811) {
		t.Errorf("resolution returned a snapshot, not the live array: got %d", (*resolved)[0])
	}
}

// TestNoFrameIsDistinctFromAZeroFrame states the invariant the fix rests on.
// "No config" must be representable as the ABSENCE of a frame; it must not be
// encoded as some in-band channel value, because every in-band value is a
// command the receiver will act on while believing the link is healthy.
func TestNoFrameIsDistinctFromAZeroFrame(t *testing.T) {
	zeros := &[16]util.CRSFValue{}
	withZeros := &map[string]*[16]util.CRSFValue{testPort: zeros}

	// A config that genuinely evaluates to zeros still transmits -- the fix
	// keys off the absence of config, not off the payload's value.
	if got := resolveChannels(withZeros, testPort); got == nil {
		t.Errorf("resolution must key off missing config, not off the values")
	}

	// ...while no config at all yields nothing to transmit.
	if got := resolveChannels(&map[string]*[16]util.CRSFValue{}, testPort); got != nil {
		t.Errorf("no config must yield no frame, got %v", *got)
	}
}

func centeredTestValues() *[16]util.CRSFValue {
	var values [16]util.CRSFValue
	for i := range values {
		values[i] = util.CRSFValue(util.CRSFCenterValue)
	}
	return &values
}
