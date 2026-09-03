// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for the W17 arm-chain lint (review finding MAP-12, owner decision
// OD-9/D2(a)).
//
// The shape these pin was previously asserted ONLY in w17_profile_test.go,
// against the copy of configs/w17-ds4.json in this repo. Race day loads a
// hand-edited copy from an absolute path on the giftee's PC, and every layer in
// between accepts an arm chain with the safety shape removed: reset_on_nan
// defaults to false in the schema, and a bare seq is perfectly valid. So the
// property whose absence produced review blocker F2 was pinned everywhere
// except where it is used.
//
// Each test removes exactly one part of the shape and requires the linter to
// name it, because a linter that only says "wrong" is a linter people bypass.

import (
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// armSeq is the toggle at the heart of the arm chain, in its correct shape.
func armSeq() *InputSeq {
	values := []util.RawValue{0, 32767}
	method := ClockwiseTraversal
	conditions := []*IOHolder{numberInput(0)}
	return &InputSeq{
		Id: "arm-toggle", Type: "seq",
		Seq: SeqT{
			Conditions:            &conditions,
			OutputValues:          &values,
			TraversalMethod:       &method,
			ActivationDurationMin: 0,
			ActivationDurationMax: 1 << 30,
			ResetOnNaN:            true,
		},
	}
}

// armChannel wires ch5 <- and(seq, [liveness probe]) -- the shape the lint
// requires. The mutator gets the gate and the toggle so a test can take one
// part of it away.
func armChannel(mutate func(gate *InputAnd, seq *InputSeq)) *IOHolder {
	seq := armSeq()
	right := []*IOHolder{numberInput(1)}
	gate := &InputAnd{
		Id: "arm-gate", Type: "and",
		And: AndT{Left: &IOHolder{IO: seq}, Right: &right},
	}
	if mutate != nil {
		mutate(gate, seq)
	}

	offRail := w17OffRail
	crsfMin, crsfMax := util.RawValue(172), util.RawValue(1811)
	rawMin, rawMax := util.MinRaw, util.MaxRaw
	return &IOHolder{IO: &InputChannel{
		Id: "ch5", Type: "channel",
		Channel: ChannelT{
			Number: w17ArmChannel, Input: &IOHolder{IO: gate},
			CRSFMin: &crsfMin, CRSFMax: &crsfMax,
			RawMin: &rawMin, RawMax: &rawMax,
			Failsafe: &offRail,
		},
	}}
}

// markedConfig is a config that declares itself the W17 profile.
func markedConfig(holders ...*IOHolder) *Config {
	cfg := newTestConfig()
	cfg.W17Profile = true
	for i, ih := range holders {
		cfg.IOMap["entry"+string(rune('a'+i))] = ih
	}
	return cfg
}

func armFindings(t *testing.T, cfg *Config) []string {
	t.Helper()
	return LintW17ArmChain(cfg)
}

func requireFinding(t *testing.T, findings []string, substr string) {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f, substr) {
			return
		}
	}
	t.Errorf("no finding mentioning %q; got %v", substr, findings)
}

// TestTheCorrectArmChainLintsClean is the floor: the shape the profile ships
// must produce nothing, or every other test here is meaningless.
func TestTheCorrectArmChainLintsClean(t *testing.T) {
	if findings := armFindings(t, markedConfig(armChannel(nil))); len(findings) != 0 {
		t.Errorf("the correct arm chain produced findings: %v", findings)
	}
}

// TestTheArmChainLintIsSilentWithoutTheMarker is the other half of OD-9/D2(a):
// an upstream rig may use channel 5 for anything at all, and this fork still
// has to load its configs.
func TestTheArmChainLintIsSilentWithoutTheMarker(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["ch"] = armChannel(func(gate *InputAnd, seq *InputSeq) {
		seq.Seq.ResetOnNaN = false
		gate.And.Right = nil
	})

	if findings := LintW17ArmChain(cfg); len(findings) != 0 {
		t.Errorf("an unmarked config was judged against the W17 arm-chain shape: %v", findings)
	}
	// And through the front door: LintConfig must not add arm-chain findings
	// either. (Endpoint findings are a different rule and stay universal.)
	for _, f := range LintConfig(cfg) {
		if strings.Contains(f, "arm") {
			t.Errorf("LintConfig warned an unmarked config about the arm chain: %s", f)
		}
	}
}

// TestAMissingResetOnNaNIsCaught is review blocker F2 itself: this is the field
// a hand-edit is most likely to lose, because the schema default is false.
func TestAMissingResetOnNaNIsCaught(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		seq.Seq.ResetOnNaN = false
	})))

	requireFinding(t, findings, "reset_on_nan")
	requireFinding(t, findings, "F2")
}

// TestAnUngatedToggleIsCaught: a naked seq HOLDS its value through a dropout,
// which is exactly what the `and` exists to prevent.
func TestAnUngatedToggleIsCaught(t *testing.T) {
	cfg := markedConfig()
	seq := armSeq()
	offRail := w17OffRail
	cfg.IOMap["ch"] = &IOHolder{IO: &InputChannel{
		Id: "ch5", Type: "channel",
		Channel: ChannelT{Number: w17ArmChannel, Input: &IOHolder{IO: seq}, Failsafe: &offRail},
	}}

	requireFinding(t, armFindings(t, cfg), "not the liveness-gating `and`")
}

// TestAnEmptyRightSideIsCaught: the gate is only a gate because the device-fed
// probe is on the RIGHT, where its nan makes the whole node nan.
func TestAnEmptyRightSideIsCaught(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(gate *InputAnd, _ *InputSeq) {
		empty := []*IOHolder{}
		gate.And.Right = &empty
	})))

	requireFinding(t, findings, "no liveness probe on its right side")
}

// TestAToggleThatBootsArmedIsCaught.
func TestAToggleThatBootsArmedIsCaught(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		values := []util.RawValue{32767, 0}
		seq.Seq.OutputValues = &values
	})))

	requireFinding(t, findings, "does not boot disarmed")
}

// TestAMissingTraversalMethodIsCaught: without one the toggle is DEAD, which
// fails safe but silently -- the car simply never arms and nothing says why.
func TestAMissingTraversalMethodIsCaught(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		seq.Seq.TraversalMethod = nil
	})))

	requireFinding(t, findings, "traversal_method")
}

// TestAWrongNumberOfOutputValuesIsCaught.
func TestAWrongNumberOfOutputValuesIsCaught(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		values := []util.RawValue{0, 16000, 32767}
		seq.Seq.OutputValues = &values
	})))

	requireFinding(t, findings, "output values")
}

// TestAMarkedProfileWithNoArmChannelIsCaught: a W17 profile that drives no ch5
// cannot arm the car at all.
func TestAMarkedProfileWithNoArmChannelIsCaught(t *testing.T) {
	cfg := markedConfig(numberChannel(1, 0))

	requireFinding(t, armFindings(t, cfg), "must drive channel 5")
}

// TestTwoArmChannelsAreCaught: which one wins is decided by map iteration
// order, so the chain cannot be checked at all -- and neither can it be
// reasoned about at the bench.
func TestTwoArmChannelsAreCaught(t *testing.T) {
	cfg := markedConfig(armChannel(nil))
	cfg.IOMap["second"] = armChannel(func(_ *InputAnd, seq *InputSeq) {
		seq.Seq.ResetOnNaN = false
	})

	requireFinding(t, armFindings(t, cfg), "map iteration order")
}

// TestAnArmChannelWithNoInputIsCaught.
func TestAnArmChannelWithNoInputIsCaught(t *testing.T) {
	cfg := markedConfig()
	offRail := w17OffRail
	cfg.IOMap["ch"] = &IOHolder{IO: &InputChannel{
		Id: "ch5", Type: "channel",
		Channel: ChannelT{Number: w17ArmChannel, Failsafe: &offRail},
	}}

	requireFinding(t, armFindings(t, cfg), "has no input")
}

// TestTheRefusalSaysWhatToDoAboutIt. The sentence is read by someone whose car
// will not move; it has to name the marker, the finding, and where the shape is
// written down.
func TestTheRefusalSaysWhatToDoAboutIt(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		seq.Seq.ResetOnNaN = false
	})))
	refusal := W17ArmChainRefusal(findings)

	for _, want := range []string{"w17_profile", "reset_on_nan", "configs/README.md"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal does not mention %q: %s", want, refusal)
		}
	}
	if W17ArmChainRefusal(nil) != "" {
		t.Error("an empty finding list must produce no sentence at all")
	}
}

// TestTheRefusalNeverOffersDeletingTheMarker pins the wording change made after
// independent review (2026-09-04). The earlier sentence ended "...or, if this
// profile is for a different rig, remove the \"w17_profile\" marker", offering a
// one-token edit as a CO-EQUAL remedy to reshaping the graph -- and that edit
// silences every arm-chain rule on the copy the car actually loads. The
// operator reads this at the bench, with the file open, at the moment the car
// will not start.
//
// The rule this pins: the refusal may say the marker belongs only on the W17
// profile, but no sentence in it may read as an instruction to remove, delete
// or drop the marker from the file being refused.
func TestTheRefusalNeverOffersDeletingTheMarker(t *testing.T) {
	findings := armFindings(t, markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		seq.Seq.ResetOnNaN = false
	})))
	refusal := W17ArmChainRefusal(findings)
	lower := strings.ToLower(refusal)

	for _, verb := range []string{"remove", "delete", "drop", "take out", "strip"} {
		for _, object := range []string{"the marker", "the \"w17_profile\" marker", "w17_profile"} {
			bad := verb + " " + object
			for at := 0; ; {
				i := strings.Index(lower[at:], bad)
				if i < 0 {
					break
				}
				i += at
				at = i + len(bad)
				// A NEGATED mention is the point of the sentence ("do not
				// delete the marker"), so only an un-negated instruction fails.
				start := i - 12
				if start < 0 {
					start = 0
				}
				before := lower[start:i]
				if strings.Contains(before, "not ") || strings.Contains(before, "never ") {
					continue
				}
				t.Errorf("the refusal tells the operator to %q (context %q), which turns the "+
					"arm-chain checks off on the one copy that matters: %s", bad, before, refusal)
			}
		}
	}

	// It must still restate the safe direction, or the sentence has lost the
	// information the wording change was meant to keep.
	if !strings.Contains(lower, "restore the arm chain") {
		t.Errorf("the refusal no longer names the safe remedy: %s", refusal)
	}
	if !strings.Contains(lower, "do not delete") {
		t.Errorf("the refusal no longer warns against deleting the marker: %s", refusal)
	}
}

// TestLintConfigCarriesTheArmChainFindingsForAMarkedProfile keeps the two
// entry points from drifting: the linter is the single place the shape lives.
func TestLintConfigCarriesTheArmChainFindingsForAMarkedProfile(t *testing.T) {
	cfg := markedConfig(armChannel(func(_ *InputAnd, seq *InputSeq) {
		seq.Seq.ResetOnNaN = false
	}))

	requireFinding(t, LintConfig(cfg), "reset_on_nan")
}
