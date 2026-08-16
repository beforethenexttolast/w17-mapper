// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package devices

// Tests for the per-direction hat decode (2026-08-16 audit, defect 13).
//
// SDL reports a hat as a BITMASK: centered 0, up 1, right 2, down 4, left 8,
// with diagonals as OR combinations. The legacy read mapped that mask through
// a [-1, 1] input range, so every pressed position clamped to the same MaxRaw
// -- direction-blind, which broke any binding that cares which way the D-pad
// points (the recorded D-pad-DOWN deadman plan included).
//
// DecodeHatDirection is deliberately a pure function of (state, direction) so
// the whole truth table can be pinned here without an SDL joystick, which a
// test cannot fabricate (the same limit the failsafe tests document).

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
	"github.com/veandco/go-sdl2/sdl"
)

func TestDecodeHatDirectionTruthTable(t *testing.T) {
	directions := map[string]uint8{
		"up":    sdl.HAT_UP,
		"right": sdl.HAT_RIGHT,
		"down":  sdl.HAT_DOWN,
		"left":  sdl.HAT_LEFT,
	}

	// Every SDL hat state, mapped to the set of directions that must read
	// truthy in it. Diagonals activate BOTH component directions.
	states := map[string]struct {
		state  uint8
		active map[string]bool
	}{
		"centered":  {sdl.HAT_CENTERED, map[string]bool{}},
		"up":        {sdl.HAT_UP, map[string]bool{"up": true}},
		"right":     {sdl.HAT_RIGHT, map[string]bool{"right": true}},
		"down":      {sdl.HAT_DOWN, map[string]bool{"down": true}},
		"left":      {sdl.HAT_LEFT, map[string]bool{"left": true}},
		"rightup":   {sdl.HAT_RIGHTUP, map[string]bool{"right": true, "up": true}},
		"rightdown": {sdl.HAT_RIGHTDOWN, map[string]bool{"right": true, "down": true}},
		"leftup":    {sdl.HAT_LEFTUP, map[string]bool{"left": true, "up": true}},
		"leftdown":  {sdl.HAT_LEFTDOWN, map[string]bool{"left": true, "down": true}},
	}

	for stateName, tc := range states {
		for dirName, dirMask := range directions {
			got := DecodeHatDirection(tc.state, dirMask)
			want := util.DefaultFalsyRawValue
			if tc.active[dirName] {
				want = util.DefaultTruthyRawValue
			}
			if got != want {
				t.Errorf("state %s, direction %s: got %d, want %d",
					stateName, dirName, got, want)
			}
		}
	}
}

// TestHatDirectionsTableMatchesSDL pins the name->mask table the config layer
// resolves against: exactly the four cardinal directions, each on its SDL bit.
func TestHatDirectionsTableMatchesSDL(t *testing.T) {
	want := map[string]uint8{
		"up":    sdl.HAT_UP,
		"right": sdl.HAT_RIGHT,
		"down":  sdl.HAT_DOWN,
		"left":  sdl.HAT_LEFT,
	}

	if len(HatDirections) != len(want) {
		t.Errorf("HatDirections has %d entries, want %d (%v)",
			len(HatDirections), len(want), HatDirections)
	}
	for name, mask := range want {
		if got, ok := HatDirections[name]; !ok || got != mask {
			t.Errorf("HatDirections[%q] = %d (present=%v), want %d", name, got, ok, mask)
		}
	}
}

// TestDistinctDirectionsAreDistinguished is the direct regression for the
// defect: two different pressed positions must be able to produce different
// outputs for the same configured direction. Under the legacy scalar read,
// every pressed position collapsed to the same value.
func TestDistinctDirectionsAreDistinguished(t *testing.T) {
	down := DecodeHatDirection(sdl.HAT_DOWN, sdl.HAT_DOWN)
	up := DecodeHatDirection(sdl.HAT_UP, sdl.HAT_DOWN)

	if down == up {
		t.Errorf("direction-blind: hat DOWN and hat UP both decode to %d for a "+
			"down binding", down)
	}
}
