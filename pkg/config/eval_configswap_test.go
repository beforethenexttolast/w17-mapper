// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for what applying a config does to the transmitted arrays.
//
// Two separate claims are pinned here, and only one of them is a fix:
//
//  1. A published map is an EVALUATED map. Publishing before evaluating exposed
//     an array in which every channel read 992 until the next device event.
//     Fixed in applyConfig.
//
//  2. A channel the new config no longer maps keeps the 992 its rebuilt array
//     was seeded with, permanently, because no node writes it. That is NOT
//     fixable here -- there is no node left to ask for a neutral, which is why
//     seeding from the configured failsafe would not have helped either. It is
//     the mechanism the send loop's configSwapGate exists to cover, and this
//     test exists so that the reason stays visible in code.

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const swapTestPort = "/dev/test"

// configWith builds a Config holding a single transmitter on swapTestPort.
func configWith(channels ...*IOHolder) *Config {
	cfg := newTestConfig("unplugged")

	list := channels
	tx := &OutputTransmitter{
		Id: "tx", Type: "transmitter",
		Transmitter: TransmitterT{Port: swapTestPort, Channels: &list},
	}
	cfg.IOMap["tx"] = &IOHolder{IO: tx, Ctl: cfg.Ctl, Config: cfg}

	return cfg
}

// TestPublishedArraysAreEvaluated covers claim 1. Without it, every channel --
// including the ones the new config maps perfectly well -- reads center from the
// moment Apply is pressed until an unrelated device event arrives.
func TestPublishedArraysAreEvaluated(t *testing.T) {
	_, published, _ := applyConfig(configWith(numberChannel(1, util.MaxRaw)))

	values, ok := published[swapTestPort]
	if !ok {
		t.Fatalf("expected the port to be published, got %v", published)
	}

	if values[0] == center {
		t.Errorf("ch1 published at center: the map was published before it was evaluated")
	}
	if values[0] != full {
		t.Errorf("expected ch1=%d, got %d", full, values[0])
	}
}

// TestDroppedChannelReadsCenterAfterASwap covers claim 2 -- the mechanism, not a
// fix. It is deliberately written as an assertion of the CURRENT behaviour: if
// this ever starts failing, the send loop's swap suppression may no longer be
// needed, and that is a decision to take deliberately rather than by accident.
func TestDroppedChannelReadsCenterAfterASwap(t *testing.T) {
	// Before the swap: ch5 is an arm channel held hard ON.
	before := configWith(numberChannel(1, util.MaxRaw), numberChannel(5, util.MaxRaw))
	_, published, _ := applyConfig(before)

	if got := published[swapTestPort][4]; got != full {
		t.Fatalf("setup: expected ch5 latched ON at %d, got %d", full, got)
	}

	// The replacement config no longer maps ch5.
	after := configWith(numberChannel(1, util.MaxRaw))
	_, published, _ = applyConfig(after)

	values := published[swapTestPort]
	if values[4] != center {
		t.Fatalf("expected the dropped ch5 to read center %d, got %d", center, values[4])
	}

	// ...and nothing brings it anywhere else, however long the link runs.
	for tick := 0; tick < 10; tick++ {
		for _, holder := range after.IOMap {
			holder.Eval(after)
		}
		if values[4] != center {
			t.Fatalf("tick %d: ch5 moved to %d", tick, values[4])
		}
	}

	// Center is not a neutral for a switch: it normalizes to 0, lands inside the
	// receiver's hysteresis dead band, and HOLDS the latched state. Nothing at
	// this layer can fix that -- the channel has no node left to carry a
	// failsafe intent.
	if center != util.CRSFValue(util.CRSFCenterValue) {
		t.Fatalf("center anchor changed")
	}
}

// TestSwapKeepsMappedChannelsLive is the over-correction guard: a swap must not
// disturb the channels the new config still maps.
func TestSwapKeepsMappedChannelsLive(t *testing.T) {
	rail := offRail

	after := configWith(channelNode(1, &IOHolder{IO: &InputNumber{
		Id: "num-node", Type: "number", Number: NumberT{Output: util.MaxRaw},
	}}, &rail))

	_, published, _ := applyConfig(after)

	if got := published[swapTestPort][0]; got != full {
		t.Errorf("expected the still-mapped ch1 to carry its live value %d, got %d", full, got)
	}
}
