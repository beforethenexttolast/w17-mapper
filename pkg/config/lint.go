// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// W17 firmware plausibility band. The control firmware treats a decoded CRSF
// channel value below 100 or above 1900 as implausible and the control as
// ABSENT (w17-control-fw lib/channels, since control-fw 91f830f). The nominal
// working range is the CRSF anchors 172..992..1811; the upstream schema
// defaults of 0/1984 sit OUTSIDE the band, which is why a default-endpoint
// config can never arm the W17 (2026-08-16 audit, defect 3).
const (
	w17PlausibleMin = util.RawValue(100)
	w17PlausibleMax = util.RawValue(1900)

	// w17OffRail is the CRSF LOW anchor, the value a switch-like channel must
	// rest at and fail to. Center (992) normalizes to 0, which sits inside the
	// firmware's ±250 switch hysteresis, so a switch parked at center HOLDS its
	// previous state -- an armed channel would stay armed through a dropout
	// (2026-08-16 audit, defect 1).
	w17OffRail = util.RawValue(172)
)

// w17SwitchChannels are the channels the W17 control firmware decodes through
// its hysteresis switch decoder (decodeSwitch): steering/throttle/pan/tilt are
// analog, ch13 is a stateless tri-state, and these six latch. Transcribed from
// w17-control-fw lib/channels ChannelMapConfig at control-fw 3f4f9b7; remap
// HERE only if the firmware map changes.
var w17SwitchChannels = map[int32]string{
	5:  "arm",
	6:  "DRS",
	7:  "gear up",
	8:  "gear down",
	11: "ERS boost",
	12: "ERS overtake",
}

// LintConfig reports W17 plausibility findings for a config: channel endpoints
// or failsafes outside the firmware band, and switch-role channels whose
// failsafe is not the OFF rail. W17 fork addition; wired into the server's
// SetConfig as WARNINGS.
//
// Warnings, not errors, on purpose: this fork still loads configs for rigs
// that are not the W17 (upstream's whole point), and the schema's own 0/1984
// defaults would make a hard failure reject half the configs the editor
// produces. The teeth for the W17 profile live in the test that loads the
// committed configs/w17-ds4.json and requires ZERO findings -- the shipped
// profile cannot drift into a warning without failing the suite.
//
// W17 fork modification (review finding MAP-12, owner decision OD-9/D2(a)): a
// config that declares the W17 marker is additionally checked for the ARM-CHAIN
// SHAPE, and those findings are FATAL at the load path rather than advisory.
// See LintW17ArmChain.
func LintConfig(c *Config) []string {
	if c == nil || c.IOMap == nil {
		return nil
	}

	var findings []string
	for _, ih := range c.IOMap {
		for _, ch := range collectChannels(ih) {
			findings = append(findings, lintChannel(ch)...)
		}
	}

	// Only for a file that claims to be the W17 profile. An upstream rig is
	// entitled to use channel 5 for anything it likes, and warning it about an
	// arm-gate shape it never asked for is exactly the crying-wolf that teaches
	// people to ignore the lint.
	findings = append(findings, LintW17ArmChain(c)...)

	// The IOMap walk order is nondeterministic; sorted findings keep logs and
	// tests stable.
	sort.Strings(findings)
	return findings
}

func lintChannel(ch *InputChannel) []string {
	var findings []string

	number := ch.Channel.Number
	min := effectiveRaw(ch.Channel.CRSFMin, util.CRSFMinValue)
	max := effectiveRaw(ch.Channel.CRSFMax, util.CRSFMaxValue)
	failsafe := effectiveRaw(ch.Channel.Failsafe, util.CRSFCenterValue)

	if min >= max {
		findings = append(findings, fmt.Sprintf(
			"channel %d: crsf_min %d is not below crsf_max %d", number, min, max))
	}

	if min < w17PlausibleMin {
		findings = append(findings, fmt.Sprintf(
			"channel %d: crsf_min %d is below the firmware plausibility floor %d -- "+
				"the W17 decodes such frames as ABSENT (use the 172/1811 anchors; "+
				"the schema's 0/1984 defaults cannot arm the car)",
			number, min, w17PlausibleMin))
	}

	if max > w17PlausibleMax {
		findings = append(findings, fmt.Sprintf(
			"channel %d: crsf_max %d is above the firmware plausibility ceiling %d -- "+
				"the W17 decodes such frames as ABSENT (use the 172/1811 anchors; "+
				"the schema's 0/1984 defaults cannot arm the car)",
			number, max, w17PlausibleMax))
	}

	if failsafe < w17PlausibleMin || failsafe > w17PlausibleMax {
		findings = append(findings, fmt.Sprintf(
			"channel %d: failsafe %d is outside the firmware plausibility band %d..%d",
			number, failsafe, w17PlausibleMin, w17PlausibleMax))
	}

	if role, isSwitch := w17SwitchChannels[number]; isSwitch && failsafe != w17OffRail {
		findings = append(findings, fmt.Sprintf(
			"channel %d (%s): failsafe %d is not the %d OFF rail -- a switch channel "+
				"that fails to a center-band value sits inside the firmware's ±250 "+
				"hysteresis and HOLDS its previous state through a dropout",
			number, role, failsafe, w17OffRail))
	}

	return findings
}

// collectChannels returns every channel node in the holder's subtree. Read
// edges are not followed: their targets are top-level entries the caller
// walks anyway, and following them would double-count.
func collectChannels(ih *IOHolder) []*InputChannel {
	var out []*InputChannel
	var walk func(ih *IOHolder)
	walk = func(ih *IOHolder) {
		if ih == nil || ih.IO == nil {
			return
		}
		if ch, ok := ih.IO.(*InputChannel); ok {
			out = append(out, ch)
			// keep walking: a channel nested under a channel is degenerate but
			// representable, and hiding it from the lint would be a hole
		}
		if children := ih.IO.Children(); children != nil {
			for _, child := range *children {
				walk(child)
			}
		}
	}
	walk(ih)
	return out
}

func effectiveRaw(v *util.RawValue, fallback util.RawValue) util.RawValue {
	if v == nil {
		return fallback
	}
	return *v
}

// w17ArmChannel is the channel the control firmware decodes as ARM.
const w17ArmChannel = int32(5)

// LintW17ArmChain reports every way a W17-marked config's arm chain departs
// from the shape that keeps the car from arming itself. It returns nothing for
// a config that does not declare the marker. W17 fork addition (review finding
// MAP-12, owner decision OD-9/D2(a)).
//
// WHY THIS MOVED OUT OF A TEST. The shape was asserted only in
// w17_profile_test.go, against the copy of configs/w17-ds4.json in THIS REPO.
// Race day does not load that copy: it loads a hand-edited file at an absolute
// path on the giftee's PC, with the two placeholders filled in by hand. Every
// layer between the two -- the schema, the read-cycle check, the endpoint lint
// -- accepts an arm chain with the safety shape edited away, because
// `reset_on_nan` defaults to false (pkg/config/schema.yaml) and a bare seq is
// perfectly valid. So the one property whose absence produced review blocker
// F2 (a gamepad dropout leaving the toggle ARMED, and the auto-reconnect
// re-arming the car with zero user input) was pinned everywhere except where it
// is actually used.
//
// THE SHAPE, and what each part is for:
//
//	channel 5  <-  and( seq{ output_values[0] == 0,
//	                         explicit traversal_method,
//	                         reset_on_nan },
//	                    <non-empty right side> )
//
// Each part carries its own weight:
//
//   - the `and` with a non-empty RIGHT side is the liveness gate. A naked seq
//     HOLDS its value when its conditions go nan, so a dropout would keep
//     transmitting "armed"; a device-fed probe on the RIGHT makes the whole
//     node nan when the pad goes away, which the channel turns into its 172
//     failsafe. It has to be the right side: an `and` SWALLOWS a nan left
//     operand (see input_and.go's note).
//   - output_values[0] == 0 is "boots disarmed", and it is also where
//     reset_on_nan sends the toggle.
//   - an explicit traversal_method, because without one seq.NextValue always
//     returns the first element and the toggle is dead -- which fails safe, but
//     silently, and the car simply never arms.
//   - reset_on_nan is F2 itself.
//
// The findings are returned in a fixed order so the refusal sentence reads the
// same way twice.
func LintW17ArmChain(c *Config) []string {
	if !c.IsW17Profile() || c.IOMap == nil {
		return nil
	}

	var arms []*InputChannel
	for _, ih := range c.IOMap {
		for _, ch := range collectChannels(ih) {
			if ch.Channel.Number == w17ArmChannel {
				arms = append(arms, ch)
			}
		}
	}

	switch len(arms) {
	case 0:
		return []string{fmt.Sprintf(
			"no arm channel: a W17 profile must drive channel %d, the firmware's arm switch",
			w17ArmChannel)}
	case 1:
	default:
		return []string{fmt.Sprintf(
			"%d channel nodes drive channel %d: which one arms the car is decided by "+
				"map iteration order, so the arm chain cannot be checked at all",
			len(arms), w17ArmChannel)}
	}

	arm := arms[0]
	if arm.Channel.Input == nil || arm.Channel.Input.IO == nil {
		return []string{fmt.Sprintf(
			"the arm channel (ch%d) has no input: it would sit on its failsafe forever",
			w17ArmChannel)}
	}

	gate, ok := arm.Channel.Input.IO.(*InputAnd)
	if !ok {
		return []string{fmt.Sprintf(
			"the arm channel (ch%d) is fed by a %q node, not the liveness-gating `and` -- "+
				"without the gate the toggle HOLDS \"armed\" through a gamepad dropout",
			w17ArmChannel, arm.Channel.Input.IO.InputType())}
	}

	var findings []string

	if gate.And.Right == nil || len(*gate.And.Right) == 0 {
		findings = append(findings, "the arm gate has no liveness probe on its right side -- "+
			"nothing makes the gate nan when the gamepad goes away, so the toggle keeps "+
			"transmitting whatever it held")
	}

	if gate.And.Left == nil || gate.And.Left.IO == nil {
		findings = append(findings, "the arm gate has no toggle on its left side")
		return findings
	}

	seq, ok := gate.And.Left.IO.(*InputSeq)
	if !ok {
		findings = append(findings, fmt.Sprintf(
			"the arm gate's left side is a %q node, not the seq toggle",
			gate.And.Left.IO.InputType()))
		return findings
	}

	if seq.Seq.OutputValues == nil || len(*seq.Seq.OutputValues) != 2 {
		findings = append(findings, fmt.Sprintf(
			"the arm toggle has %d output values, want exactly 2 (disarmed, armed)",
			len(derefValues(seq.Seq.OutputValues))))
	} else if (*seq.Seq.OutputValues)[0] != 0 {
		findings = append(findings, fmt.Sprintf(
			"the arm toggle does not boot disarmed: output_values[0] = %d, want 0",
			(*seq.Seq.OutputValues)[0]))
	}

	if !seq.Seq.TraversalMethod.IsValid() {
		findings = append(findings, "the arm toggle has no explicit traversal_method -- "+
			"seq.NextValue then always returns the first element and the toggle is dead, "+
			"so the car can never arm")
	}

	if !seq.Seq.ResetOnNaN {
		findings = append(findings, "the arm toggle does not set reset_on_nan -- the toggle "+
			"HOLDS armed through a gamepad dropout, and the pad's auto-reconnect re-arms "+
			"the car with zero user input (review blocker F2)")
	}

	return findings
}

func derefValues(v *[]util.RawValue) []util.RawValue {
	if v == nil {
		return nil
	}
	return *v
}

// W17ArmChainRefusal is the plain-language sentence the load path shows when a
// W17-marked profile fails LintW17ArmChain. W17 fork addition (MAP-12).
//
// It names the marker, because the marker is what turned advice into a refusal.
// It does NOT offer deleting the marker as a remedy, and that asymmetry is the
// whole point of the wording (independent-review blocker, 2026-09-04): this
// sentence is read by whoever is setting the car up on the giftee's PC, at the
// moment the car will not start, and deleting one token from the file in front
// of them silences every arm-chain rule on the only copy that matters -- which
// is exactly the failure MAP-12 exists to close. Empirically confirmed at the
// time: the same filled profile with reset_on_nan false and the marker deleted
// is ACCEPTED and applied.
//
// The information an upstream rig needs is still here, but conditional and
// second: the marker belongs on the W17 car's profile, so a config for another
// rig should not have carried it in the first place -- that is a statement
// about a file this is not, not an instruction about the file being refused.
func W17ArmChainRefusal(findings []string) string {
	if len(findings) == 0 {
		return ""
	}

	return fmt.Sprintf("this profile declares itself the W17 race-day profile "+
		"(\"w17_profile\": true), so its arm chain has to be the shape that stops the car "+
		"arming itself -- and it is not: %s. If this is the W17 car's own profile, restore "+
		"the arm chain (see configs/README.md); do NOT delete the \"w17_profile\" marker to "+
		"get past this -- the marker is what turns these checks on, and without them the "+
		"car can arm itself after a controller dropout. The marker belongs only on the W17 "+
		"car's profile: a saved profile for some other rig should never have carried it.",
		strings.Join(findings, "; "))
}
