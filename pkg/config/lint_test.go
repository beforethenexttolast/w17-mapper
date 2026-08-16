// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for the W17 plausibility lint (2026-08-16 audit, defects 1 and 3).
//
// Defect 3: the schema's default endpoints are 0/1984, which sit OUTSIDE the
// firmware's 100/1900 plausibility band -- frames built from a default config
// decode as ABSENT, so a default config can never arm the car, silently.
//
// Defect 1: the failsafe default is center (992), which normalizes into the
// firmware's ±250 switch hysteresis, so a switch channel that fails to center
// HOLDS its previous state -- arm stays armed through a gamepad dropout.
//
// The lint turns both silences into loud load-time warnings; the committed
// W17 profile is held to zero findings in w17_profile_test.go.

import (
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// lintChannelNode builds a bare channel entry with explicit endpoints.
func lintChannelNode(number int32, crsfMin, crsfMax, failsafe util.RawValue) *IOHolder {
	rawMin, rawMax := util.MinRaw, util.MaxRaw
	return &IOHolder{IO: &InputChannel{
		Id: "ch", Type: "channel",
		Channel: ChannelT{
			Number: number, Input: numberInput(0),
			CRSFMin: &crsfMin, CRSFMax: &crsfMax,
			RawMin: &rawMin, RawMax: &rawMax,
			Failsafe: &failsafe,
		},
	}}
}

func lintOf(t *testing.T, holders ...*IOHolder) []string {
	t.Helper()
	cfg := newTestConfig()
	for i, ih := range holders {
		cfg.IOMap["entry"+string(rune('a'+i))] = ih
	}
	return LintConfig(cfg)
}

// TestLintFlagsDefaultEndpoints is the defect-3 regression: the 0/1984 schema
// defaults must produce band findings for both edges.
func TestLintFlagsDefaultEndpoints(t *testing.T) {
	findings := lintOf(t, lintChannelNode(1,
		util.CRSFMinValue, util.CRSFMaxValue, util.CRSFCenterValue))

	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (floor and ceiling), got %d: %v",
			len(findings), findings)
	}
	for _, f := range findings {
		if !strings.Contains(f, "cannot arm") {
			t.Errorf("a band finding should say the consequence, got: %q", f)
		}
	}
}

// TestLintFlagsCenterFailsafeOnSwitchChannels is the defect-1 regression: on
// each of the six firmware decodeSwitch channels, a center failsafe must be
// flagged as a latch hazard.
func TestLintFlagsCenterFailsafeOnSwitchChannels(t *testing.T) {
	for _, number := range []int32{5, 6, 7, 8, 11, 12} {
		findings := lintOf(t, lintChannelNode(number, 172, 1811, util.CRSFCenterValue))
		if len(findings) != 1 {
			t.Errorf("ch%d: expected exactly the switch-failsafe finding, got %v",
				number, findings)
			continue
		}
		if !strings.Contains(findings[0], "OFF rail") ||
			!strings.Contains(findings[0], "hysteresis") {
			t.Errorf("ch%d: the finding should explain the latch hazard, got: %q",
				number, findings[0])
		}
	}
}

// TestLintAcceptsW17ShapedChannels is the negative space: W17 anchors with the
// correct per-role failsafes produce no findings.
func TestLintAcceptsW17ShapedChannels(t *testing.T) {
	findings := lintOf(t,
		lintChannelNode(1, 172, 1811, 992),  // proportional: center failsafe
		lintChannelNode(5, 172, 1811, 172),  // switch: OFF rail
		lintChannelNode(13, 172, 1811, 172), // tri-state: LOW = TRAINING
	)
	if len(findings) != 0 {
		t.Errorf("expected no findings for W17-shaped channels, got %v", findings)
	}
}

// TestLintLeavesProportionalCenterAlone: 992 is the RIGHT failsafe for a
// non-switch channel; only the six switch channels demand the rail.
func TestLintLeavesProportionalCenterAlone(t *testing.T) {
	for _, number := range []int32{1, 3, 9, 10} {
		if findings := lintOf(t, lintChannelNode(number, 172, 1811, 992)); len(findings) != 0 {
			t.Errorf("ch%d: center failsafe on a proportional channel is correct, got %v",
				number, findings)
		}
	}
}

// TestLintFlagsOutOfBandFailsafe: a failsafe the firmware would reject as
// implausible defeats its own purpose.
func TestLintFlagsOutOfBandFailsafe(t *testing.T) {
	findings := lintOf(t, lintChannelNode(1, 172, 1811, 0))
	if len(findings) != 1 || !strings.Contains(findings[0], "failsafe") {
		t.Errorf("expected the out-of-band failsafe finding, got %v", findings)
	}
}

// TestLintFlagsInvertedEndpoints: min not below max is a broken mapping.
func TestLintFlagsInvertedEndpoints(t *testing.T) {
	findings := lintOf(t, lintChannelNode(1, 1811, 172, 992))
	found := false
	for _, f := range findings {
		if strings.Contains(f, "not below") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the inverted-endpoints finding, got %v", findings)
	}
}

// TestLintFindsNestedChannels: channels live inside transmitter entries in
// every real config; the walk must reach them there.
func TestLintFindsNestedChannels(t *testing.T) {
	cfg := newTestConfig()
	list := []*IOHolder{lintChannelNode(5, 172, 1811, 992)}
	cfg.IOMap["tx"] = &IOHolder{IO: &OutputTransmitter{
		Id: "tx", Type: "tx",
		Transmitter: TransmitterT{Port: "/dev/test", Channels: &list},
	}}

	if findings := LintConfig(cfg); len(findings) != 1 {
		t.Errorf("expected the nested switch channel to be found, got %v", findings)
	}
}

// TestLintNilSafety mirrors CheckReadCycles: bare and nil configs are quiet.
func TestLintNilSafety(t *testing.T) {
	if findings := LintConfig(nil); findings != nil {
		t.Errorf("nil config: %v", findings)
	}
	if findings := LintConfig(&Config{}); findings != nil {
		t.Errorf("nil IOMap: %v", findings)
	}
}
