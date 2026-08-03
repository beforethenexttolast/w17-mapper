// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package link

// Tests for the third suppression cause: the config layer reporting that it
// cannot account for every channel a port drives.
//
// The condition behind it lives in pkg/config. OutputTransmitter.Eval walks a
// top-level entry's subtree to find the channel nodes it can drive, and that walk
// is depth-bounded. If the bound truncates it, the owner set is incomplete: some
// channel may be left holding a stale value with nothing able to neutralize it,
// because the code no longer knows that channel exists. That is unknown state,
// and this fork has already answered unknown state on this path twice -- 630ea96
// for no-config, configSwapGate for a config swap -- by transmitting nothing and
// letting the receiver's own link-loss failsafe resolve it. Same answer here.
//
// What these tests are really pinning is the COMPOSITION. Three independent
// causes now share one outcome, and the property that matters is that none of
// them can end a suppression another is holding open.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

func unresolvedMap(port string, unresolved bool) *map[string]*atomic.Bool {
	flag := &atomic.Bool{}
	flag.Store(unresolved)
	return &map[string]*atomic.Bool{port: flag}
}

// TestUnresolvedPortSuppresses is the case itself.
func TestUnresolvedPortSuppresses(t *testing.T) {
	if !portUnresolved(unresolvedMap(testPort, true), testPort) {
		t.Errorf("a port the config layer cannot account for must read as unresolved")
	}

	if reason := suppressionReason(centeredTestValues(), false, true); reason == "" {
		t.Errorf("an unresolved port must suppress even though its config resolves and no " +
			"swap window is open")
	}
}

// TestResolvedPortTransmits is the over-correction guard, and it is the one that
// matters most: this flag sits on the send path, so a stuck-true reading would
// silently stop the car being driveable at all.
func TestResolvedPortTransmits(t *testing.T) {
	if portUnresolved(unresolvedMap(testPort, false), testPort) {
		t.Errorf("a port the config layer can account for must not read as unresolved")
	}

	if reason := suppressionReason(centeredTestValues(), false, false); reason != "" {
		t.Errorf("a resolved port with a live config and no swap window must transmit, "+
			"got suppressed for %q", reason)
	}
}

// TestUnresolvedMapAbsenceIsNotUnresolved covers the states the map passes
// through before and around a config. A nil map is the send loop starting before
// anything is published; a missing entry is a config that maps other ports. Both
// are already covered by resolveChannels finding no array, and neither is a
// statement that this port is unaccounted for.
func TestUnresolvedMapAbsenceIsNotUnresolved(t *testing.T) {
	if portUnresolved(nil, testPort) {
		t.Errorf("a nil map must not read as unresolved")
	}

	if portUnresolved(&map[string]*atomic.Bool{}, testPort) {
		t.Errorf("a map with no entry for this port must not read as unresolved")
	}

	if portUnresolved(&map[string]*atomic.Bool{testPort: nil}, testPort) {
		t.Errorf("a nil flag must not read as unresolved")
	}

	if portUnresolved(unresolvedMap("/dev/ttyOTHER", true), testPort) {
		t.Errorf("another port being unaccounted for must not suppress this one")
	}
}

// TestSuppressionCausesCompose is the review question asked directly: two
// mechanisms coexisted before this change and now there are three, so check that
// each one alone suppresses and that no combination lets a frame through.
func TestSuppressionCausesCompose(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noConfig   bool
		swap       bool
		unresolved bool
	}{
		{"no config alone", true, false, false},
		{"swap alone", false, true, false},
		{"unresolved alone", false, false, true},
		{"unresolved during a swap window", false, true, true},
		{"unresolved with no config", true, false, true},
		{"swap with no config", true, true, false},
		{"all three", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var values *[16]util.CRSFValue
			if !tc.noConfig {
				values = centeredTestValues()
			}

			if reason := suppressionReason(values, tc.swap, tc.unresolved); reason == "" {
				t.Errorf("expected suppression, got a transmitted frame")
			}
		})
	}

	if reason := suppressionReason(centeredTestValues(), false, false); reason != "" {
		t.Errorf("with no cause holding, the loop must transmit; got %q", reason)
	}
}

// TestUnresolvedOutlastsAnExpiredSwapWindow is the "never a timeout" property
// applied to the new cause. A swap window closing must not resume transmission
// while the config layer still cannot account for the port's channels.
func TestUnresolvedOutlastsAnExpiredSwapWindow(t *testing.T) {
	now := time.Now()

	gate := &configSwapGate{}
	gate.observe(&map[string]*[16]util.CRSFValue{testPort: centeredTestValues()}, now)

	now = now.Add(configSwapFailsafeWindow + time.Second)
	if gate.holdOff(now) {
		t.Fatalf("setup: expected the swap window to have closed")
	}

	if reason := suppressionReason(gate.values(testPort), gate.holdOff(now), true); reason == "" {
		t.Errorf("the swap window closing resumed transmission while the port was still " +
			"unaccounted for; a window may only ever lengthen a no-frame gap, never end one")
	}
}

// TestUnresolvedDoesNotLatch is the recovery half. Suppression here is a
// statement about the current config, so a config the walk can traverse must
// transmit again.
func TestUnresolvedDoesNotLatch(t *testing.T) {
	flag := &atomic.Bool{}
	published := &map[string]*atomic.Bool{testPort: flag}

	flag.Store(true)
	if !portUnresolved(published, testPort) {
		t.Fatalf("setup: expected suppression")
	}

	flag.Store(false)
	if portUnresolved(published, testPort) {
		t.Errorf("suppression latched: the send loop reads through the published pointer " +
			"every tick, so clearing the flag must resume transmission without republishing")
	}
}
