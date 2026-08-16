// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"sort"

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
