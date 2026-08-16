// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests pinning the committed W17 DualShock 4 profile, configs/w17-ds4.json
// (2026-08-16 audit, defects 1 and 3; channel map transcribed from
// w17-control-fw lib/channels ChannelMapConfig at control-fw 3f4f9b7).
//
// The profile is a REAL artifact the race-day bring-up loads through
// -config-file-path, so it is held to the same bar as code:
//   - schema-valid, cycle-free, ZERO lint findings;
//   - every channel on the 172/1811 anchors (defect 3: the 0/1984 defaults
//     decode as absent -- a default config cannot arm);
//   - the OFF rail failsafe on every firmware decodeSwitch channel (defect 1:
//     a center failsafe latches through the receiver's hysteresis);
//   - SHARE (button 8), OPTIONS (button 9) and the D-pad UNBOUND -- reserved
//     for the recorded head-tracking affordances (Alternative C).

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const w17ProfilePath = "../../configs/w17-ds4.json"

// DS4 buttons the profile must leave alone: SHARE and OPTIONS (SDL buttons 8
// and 9 in the layout the profile binds) are reserved for the head-tracking
// affordances, alongside D-pad DOWN -- covered by requiring NO hat nodes at
// all.
var w17ReservedButtons = map[int32]string{
	8: "SHARE",
	9: "OPTIONS",
}

func loadW17Profile(t *testing.T) (*Config, []byte) {
	t.Helper()

	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("the committed W17 profile must exist: %v", err)
	}

	tmp := struct {
		Config *Config `json:"config"`
	}{}
	if err := json.Unmarshal(raw, &tmp); err != nil {
		t.Fatalf("profile does not decode: %v", err)
	}
	if tmp.Config == nil {
		t.Fatalf("profile has no config object")
	}
	return tmp.Config, raw
}

func TestW17ProfileValidatesAndLintsClean(t *testing.T) {
	cfg, raw := loadW17Profile(t)

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("profile is not JSON: %v", err)
	}
	if err := GetSchema().Validate(doc); err != nil {
		t.Errorf("profile must be schema-valid: %v", err)
	}

	if err := cfg.CheckReadCycles(); err != nil {
		t.Errorf("profile must be cycle-free: %v", err)
	}

	if findings := LintConfig(cfg); len(findings) != 0 {
		t.Errorf("the shipped profile must lint clean, got:\n  %v", findings)
	}
}

func TestW17ProfileChannelMap(t *testing.T) {
	cfg, _ := loadW17Profile(t)

	channels := map[int32]*InputChannel{}
	for _, ih := range cfg.IOMap {
		for _, ch := range collectChannels(ih) {
			if _, dup := channels[ch.Channel.Number]; dup {
				t.Errorf("channel %d is mapped twice", ch.Channel.Number)
			}
			channels[ch.Channel.Number] = ch
		}
	}

	// The full firmware map: analog, the six decodeSwitch channels, and the
	// tri-state drive mode. ch2/ch4 and 14..16 are unmapped on the firmware
	// side too.
	wantFailsafe := map[int32]util.RawValue{
		1:  992, // steering: center
		3:  992, // throttle: center
		5:  172, // arm: OFF rail
		6:  172, // DRS: closed
		7:  172, // gear up: released
		8:  172, // gear down: released
		9:  992, // pan: center
		10: 992, // tilt: center
		11: 172, // boost: OFF rail
		12: 172, // overtake: OFF rail
		13: 172, // drive mode: LOW = TRAINING, the gentle default
	}

	if len(channels) != len(wantFailsafe) {
		t.Errorf("profile maps %d channels, want %d", len(channels), len(wantFailsafe))
	}

	for number, want := range wantFailsafe {
		ch, ok := channels[number]
		if !ok {
			t.Errorf("channel %d is missing from the profile", number)
			continue
		}

		if got := effectiveRaw(ch.Channel.CRSFMin, util.CRSFMinValue); got != 172 {
			t.Errorf("ch%d: crsf_min = %d, want the 172 anchor", number, got)
		}
		if got := effectiveRaw(ch.Channel.CRSFMax, util.CRSFMaxValue); got != 1811 {
			t.Errorf("ch%d: crsf_max = %d, want the 1811 anchor", number, got)
		}
		if got := effectiveRaw(ch.Channel.Failsafe, util.CRSFCenterValue); got != want {
			t.Errorf("ch%d: failsafe = %d, want %d", number, got, want)
		}
	}
}

// TestW17ProfileLeavesReservedInputsUnbound walks every node in the profile:
// no button node may reference SHARE or OPTIONS, and no hat node may exist at
// all (the D-pad, DOWN included, belongs to the head-tracking affordances).
func TestW17ProfileLeavesReservedInputsUnbound(t *testing.T) {
	cfg, _ := loadW17Profile(t)

	var walk func(ih *IOHolder)
	walk = func(ih *IOHolder) {
		if ih == nil || ih.IO == nil {
			return
		}
		switch node := ih.IO.(type) {
		case *InputButton:
			if name, reserved := w17ReservedButtons[node.Button.Number]; reserved {
				t.Errorf("button %d (%s) is bound by %q -- it is reserved for the "+
					"head-tracking affordances", node.Button.Number, name, node.Id)
			}
		case *InputHat:
			t.Errorf("hat node %q found -- the D-pad is reserved for the "+
				"head-tracking affordances and must stay unbound", node.Id)
		}
		if children := ih.IO.Children(); children != nil {
			for _, child := range *children {
				walk(child)
			}
		}
	}
	for _, ih := range cfg.IOMap {
		walk(ih)
	}
}

// TestW17ProfileArmChainShape pins the two properties that make the arm
// binding safe, so an editor session cannot quietly simplify them away:
//   - the toggle boots DISARMED (seq output_values[0] == 0);
//   - the toggle is liveness-gated (an `and` with a non-empty right side),
//     because a naked seq HOLDS its state when its conditions go nan -- a
//     dropout would keep transmitting "armed" (see input_seq.go's allNan
//     branch).
func TestW17ProfileArmChainShape(t *testing.T) {
	cfg, _ := loadW17Profile(t)

	var arm *InputChannel
	for _, ih := range cfg.IOMap {
		for _, ch := range collectChannels(ih) {
			if ch.Channel.Number == 5 {
				arm = ch
			}
		}
	}
	if arm == nil || arm.Channel.Input == nil {
		t.Fatal("no arm channel (ch5) in the profile")
	}

	gate, ok := arm.Channel.Input.IO.(*InputAnd)
	if !ok {
		t.Fatalf("ch5 input is %T, want the liveness-gating `and`", arm.Channel.Input.IO)
	}
	if gate.And.Right == nil || len(*gate.And.Right) == 0 {
		t.Fatal("the arm gate has no liveness probe on its right side")
	}
	if gate.And.Left == nil {
		t.Fatal("the arm gate has no toggle on its left side")
	}

	seq, ok := gate.And.Left.IO.(*InputSeq)
	if !ok {
		t.Fatalf("arm gate left is %T, want the seq toggle", gate.And.Left.IO)
	}
	if seq.Seq.OutputValues == nil || len(*seq.Seq.OutputValues) != 2 {
		t.Fatalf("arm toggle should have exactly 2 output values, got %v", seq.Seq.OutputValues)
	}
	if (*seq.Seq.OutputValues)[0] != 0 {
		t.Errorf("the arm toggle must BOOT DISARMED: output_values[0] = %d, want 0",
			(*seq.Seq.OutputValues)[0])
	}
	if !seq.Seq.TraversalMethod.IsValid() {
		t.Error("the arm toggle needs an explicit traversal_method: without one, " +
			"seq.NextValue always returns the first element and the toggle is dead")
	}
}
