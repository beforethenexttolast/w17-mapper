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

// DS4 buttons the profile must leave alone, in the SDL HIDAPI PS4 layout the
// profile binds (joystick buttons in GameController order: cross 0, circle 1,
// square 2, triangle 3, SHARE 4, PS 5, OPTIONS 6, L3 7, R3 8, L1 9, R1 10).
// SHARE and OPTIONS are reserved for the head-tracking affordances, alongside
// D-pad DOWN -- the D-pad is covered by requiring NO hat nodes at all. The
// reservation is by PHYSICAL button: if a bench check finds the pad
// enumerating through the DirectInput fallback instead, the indices of BOTH
// the bindings and this reserved set move together (see configs/README.md).
var w17ReservedButtons = map[int32]string{
	4: "SHARE",
	6: "OPTIONS",
}

// w17BoundButtons is the exact set of buttons the profile may bind, HIDAPI
// order: SQUARE 2 (DRS), TRIANGLE 3 (arm), L1 9 (gear down), R1 10 (gear up).
// Pinned as a SET so a rebinding to a wrong index -- the review blocker F1
// was exactly that, raw-HID button numbers paired with HIDAPI axis numbers --
// fails loudly here instead of at the bench.
var w17BoundButtons = map[int32]string{
	2:  "SQUARE (DRS)",
	3:  "TRIANGLE (arm)",
	9:  "L1 (gear down)",
	10: "R1 (gear up)",
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

// TestW17ProfileShipsBothPlaceholders is the anti-leak pin (MAP-5). The two
// machine-specific values MUST still be placeholders in the committed file:
// a bench-filled copy pushed by accident would ship one operator's COM port
// and pad id to the giftee, where they resolve to nothing -- and, worse, would
// silently disable the refusal that tells the next operator to fill them in.
func TestW17ProfileShipsBothPlaceholders(t *testing.T) {
	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	found := UnfilledPlaceholders(doc)
	want := map[string]bool{
		"REPLACE-WITH-COM-PORT": false,
		"REPLACE-WITH-DS4-ID":   false,
	}
	for _, value := range found {
		if _, expected := want[value]; !expected {
			t.Errorf("the profile carries an unexpected placeholder %q", value)
			continue
		}
		want[value] = true
	}
	for value, seen := range want {
		if !seen {
			t.Errorf("the shipped profile must still carry %q -- a filled-in copy "+
				"must never be committed", value)
		}
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
// no button node may reference SHARE or OPTIONS, no hat node may exist at all
// (the D-pad, DOWN included, belongs to the head-tracking affordances), and
// the buttons that ARE bound must be exactly the four intended ones -- a
// binding on any unexpected index means the profile has drifted off the
// HIDAPI layout it documents.
func TestW17ProfileLeavesReservedInputsUnbound(t *testing.T) {
	cfg, _ := loadW17Profile(t)

	bound := map[int32][]string{}

	var walk func(ih *IOHolder)
	walk = func(ih *IOHolder) {
		if ih == nil || ih.IO == nil {
			return
		}
		switch node := ih.IO.(type) {
		case *InputButton:
			bound[node.Button.Number] = append(bound[node.Button.Number], node.Id)
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

	for number, ids := range bound {
		if _, expected := w17BoundButtons[number]; !expected {
			t.Errorf("button %d is bound by %v but is not in the documented "+
				"HIDAPI binding set %v", number, ids, w17BoundButtons)
		}
	}
	for number, role := range w17BoundButtons {
		if _, ok := bound[number]; !ok {
			t.Errorf("button %d (%s) is documented as bound but no node binds it",
				number, role)
		}
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
	if !seq.Seq.ResetOnNaN {
		t.Error("the arm toggle must set reset_on_nan: without it, the toggle " +
			"holds ARMED through a dropout and a pad auto-reconnect re-arms ch5 " +
			"with zero user input (review blocker F2)")
	}
}
