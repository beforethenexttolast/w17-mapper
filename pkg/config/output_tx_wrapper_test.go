// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression tests for the WRAPPER half of the failsafe gap in
// OutputTransmitter.Eval.
//
// The earlier fix (see output_tx_holdlast_test.go) drove a nan channel to its
// configured failsafe instead of skipping it, and was verified against configs
// whose transmitter `channels` entries are all `channel` nodes. That is the
// shape the UI steers toward and the only shape those tests build -- which is
// exactly why the gap below survived a green suite.
//
// `channels` is schema-typed as the full input union (schema.yaml: $ref
// '#/definitions/input'; the `expected: channel` alongside it is $meta, enforced
// nowhere in Go), so any node type may sit at the top level. Classified by what
// they do with the channel number, the 27 node types split:
//
//   - 7 always report -1 (axis, button, hat, gamepad, invert, number, seq) and
//     1 reports its own on every path (channel). Neither can strand a slot.
//   - 14 are ASYMMETRIC -- they propagate the child's number while healthy and
//     return -1 once the input stops resolving: linear, map, case, if, trim,
//     switch, and, or, eq, neq, gt, gte, lt, lte. The healthy tick writes the
//     slot; the failing tick was skipped by `ch < 1`, so the slot kept its last
//     value. That is the original hold-last defect, one level up. (D-1)
//   - 4 are transparent on BOTH paths (add, subtract, min, max, via
//     EvalOperation): the slot was written, but the neutral was resolved by
//     type-asserting FailsafeValuer on the TOP-LEVEL holder -- a wrapper is not
//     one -- so a correctly configured OFF rail was silently replaced by center,
//     which a receiver's switch hysteresis HOLDS. (D-2)
//   - 1 is pass-through (read) and inherits its target's class, while its
//     Children() is nil, so it hides its channel from any generic traversal.
//
// The fix resolves the neutral from the node that OWNS the channel number
// rather than from the holder, by collecting the InputChannels under the holder
// and driving each to its own FailsafeValue.

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// full is a healthy full-deflection reading -- the value the reported defect
// froze throttle at.
const full = util.CRSFValue(util.CRSFMaxValue)

func rawPtr(v util.RawValue) *util.RawValue { return &v }

// linearNode wraps a child in an identity `linear`. Identity ranges keep the
// test about the channel number, not about the mapping arithmetic.
func linearNode(input *IOHolder) *IOHolder {
	return &IOHolder{IO: &InputLinear{
		Id: "linear-node", Type: "linear",
		Linear: LinearT{
			Input:     input,
			InputMin:  util.RawValue(util.CRSFMinValue),
			InputMax:  util.RawValue(util.CRSFMaxValue),
			OutputMin: util.RawValue(util.CRSFMinValue),
			OutputMax: util.RawValue(util.CRSFMaxValue),
		},
	}}
}

// TestWrapperDoesNotStrandItsChannel is the reported defect reproduced through a
// top-level wrapper: full-deflection throttle frozen across a dropout, on a
// schema-valid config that the earlier fix does not reach.
func TestWrapperDoesNotStrandItsChannel(t *testing.T) {
	cfg := newTestConfig("unplugged")

	// Tick 1: healthy, full deflection, written through the wrapper.
	tx := newTx(linearNode(numberChannel(1, util.MaxRaw)))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != full {
		t.Fatalf("setup: expected ch1=%d after a healthy tick, got %d", full, got)
	}

	// Tick 2+: the gamepad is unplugged. The wrapper now reports ch = -1.
	*tx.Transmitter.Channels = []*IOHolder{linearNode(axisChannel(1, "unplugged", nil))}

	for tick := 0; tick < 5; tick++ {
		tx.Eval(cfg)
		if got := (*tx.Values)[0]; got != center {
			t.Fatalf("tick %d: ch1 stranded at %d, expected center %d "+
				"(a wrapper reporting -1 must not leave the slot holding its last value)",
				tick, got, center)
		}
	}
}

// TestWrapperReportsNoChannelWhenNaN pins the asymmetry itself, so a future
// change to the node types is caught here rather than through its effect.
func TestWrapperReportsNoChannelWhenNaN(t *testing.T) {
	cfg := newTestConfig("unplugged")

	_, _, healthyCh, healthyNaN := linearNode(numberChannel(4, util.MaxRaw)).Eval(cfg)
	if healthyNaN || healthyCh != 4 {
		t.Fatalf("setup: expected a healthy wrapper to propagate ch4, got ch=%d nan=%v",
			healthyCh, healthyNaN)
	}

	_, _, nanCh, nan := linearNode(axisChannel(4, "unplugged", nil)).Eval(cfg)
	if !nan {
		t.Fatalf("expected nan once the device stops resolving")
	}
	if nanCh >= 1 {
		t.Fatalf("the premise of this file no longer holds: the wrapper now reports "+
			"ch=%d on the nan path, so the stranding class has changed", nanCh)
	}
}

// TestRelationalWrapperDoesNotStrand covers the comparison family, which
// reaches the same skip through EvalRelational rather than its own _Eval.
func TestRelationalWrapperDoesNotStrand(t *testing.T) {
	cfg := newTestConfig("unplugged")

	gt := func(left *IOHolder) *IOHolder {
		return &IOHolder{IO: &InputGt{
			Id: "gt-node", Type: "gt",
			Gt: GtT{
				Left:         left,
				RightDefault: rawPtr(0),
				OutputTrue:   rawPtr(util.RawValue(util.CRSFMaxValue)),
				OutputFalse:  rawPtr(util.RawValue(util.CRSFMinValue)),
			},
		}}
	}

	tx := newTx(gt(numberChannel(1, util.MaxRaw)))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != full {
		t.Fatalf("setup: expected ch1=%d, got %d", full, got)
	}

	*tx.Transmitter.Channels = []*IOHolder{gt(axisChannel(1, "unplugged", nil))}
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != center {
		t.Errorf("a comparison node stranded ch1 at %d, expected center %d", got, center)
	}
}

// TestLogicalWrapperDoesNotStrand covers `and`/`or`, which the pre-merge review
// did not list. Note the channel node sits in the LAST right slot: `and`
// reassigns the reported number from each right operand in turn, so which
// number it propagates is operand-order dependent.
func TestLogicalWrapperDoesNotStrand(t *testing.T) {
	cfg := newTestConfig("unplugged")

	and := func(right *IOHolder) *IOHolder {
		rights := []*IOHolder{right}
		return &IOHolder{IO: &InputAnd{
			Id: "and-node", Type: "and",
			And: AndT{
				Right:       &rights,
				OutputTrue:  rawPtr(util.RawValue(util.CRSFMaxValue)),
				OutputFalse: rawPtr(util.RawValue(util.CRSFMinValue)),
			},
		}}
	}

	tx := newTx(and(numberChannel(1, util.MaxRaw)))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != full {
		t.Fatalf("setup: expected ch1=%d, got %d", full, got)
	}

	*tx.Transmitter.Channels = []*IOHolder{and(axisChannel(1, "unplugged", nil))}
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != center {
		t.Errorf("a logical node stranded ch1 at %d, expected center %d", got, center)
	}
}

// TestSwitchStrandsWithoutNaN is the case that shows nan is the wrong thing to
// key off. `switch` never returns nan when it has a default: when no case
// matches it returns that default with ch = -1, so the old `ch < 1` skip left
// the slot stale on a perfectly non-nan tick. Nothing is written for ch = -1
// either way, so no config could ever have observed that default on the wire.
func TestSwitchStrandsWithoutNaN(t *testing.T) {
	cfg := newTestConfig()

	// A non-case entry is truthy while its value is non-zero, and skipped once
	// it reads 0 -- so this switches between "a case matched" and "none did"
	// without ever going nan. A channel maps MinRaw onto CRSF 0, which is the
	// falsy reading; raw 0 would map onto CENTER and stay truthy.
	sw := func(caseValue util.RawValue) *IOHolder {
		cases := []*IOHolder{numberChannel(3, caseValue)}
		return &IOHolder{IO: &InputSwitch{
			Id: "switch-node", Type: "switch",
			Switch: SwitchT{Cases: &cases, OutputDefault: rawPtr(util.MinRaw)},
		}}
	}

	tx := newTx(sw(util.MaxRaw))
	tx.Eval(cfg)

	if got := (*tx.Values)[2]; got != full {
		t.Fatalf("setup: expected ch3=%d, got %d", full, got)
	}

	entry := sw(util.MinRaw)
	if _, _, ch, nan := entry.Eval(cfg); nan || ch >= 1 {
		t.Fatalf("setup: expected the fallthrough to report ch=-1 without nan, got ch=%d nan=%v",
			ch, nan)
	}

	*tx.Transmitter.Channels = []*IOHolder{entry}
	tx.Eval(cfg)

	if got := (*tx.Values)[2]; got != center {
		t.Errorf("switch fallthrough stranded ch3 at %d, expected center %d", got, center)
	}
}

// TestTransparentWrapperKeepsTheConfiguredRail is D-2, and it is the arm-safety
// one: the slot IS written, with the wrong value. 992 sits inside a receiver's
// hysteresis dead band, so an arm channel neutralized to center stays latched in
// whatever state it was in -- the configured OFF rail must survive the wrapper.
func TestTransparentWrapperKeepsTheConfiguredRail(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	add := &IOHolder{IO: &InputAdd{
		Id: "add-node", Type: "add",
		Add: AddT{Left: axisChannel(5, "unplugged", &rail), RightDefault: rawPtr(0)},
	}}

	if _, _, ch, nan := add.Eval(cfg); !nan || ch != 5 {
		t.Fatalf("setup: expected the operation family to carry ch5 through the nan, "+
			"got ch=%d nan=%v", ch, nan)
	}

	tx := newTx(add)
	tx.Eval(cfg)

	if got := (*tx.Values)[4]; got != util.CRSFValue(offRail) {
		t.Errorf("expected the arm channel's configured OFF rail %d, got %d", offRail, got)
	}
	if got := (*tx.Values)[4]; got == center {
		t.Errorf("center leaves an arm channel latched inside the receiver's hysteresis band")
	}
}

// TestSubtreeWithSeveralChannelsNeutralizesAll is why the neutral cannot be
// resolved by matching the number the holder reported. EvalOperation reports the
// LAST right operand's number while healthy but the LEFT one on the nan path, so
// keying off the reported number would neutralize ch1 and strand ch2 -- the same
// defect, moved one operand over.
func TestSubtreeWithSeveralChannelsNeutralizesAll(t *testing.T) {
	cfg := newTestConfig("unplugged")

	leftRail, rightRail := offRail, util.RawValue(200)

	healthyRight := []*IOHolder{numberChannel(2, util.MaxRaw)}
	tx := newTx(&IOHolder{IO: &InputAdd{
		Id: "add-node", Type: "add",
		Add: AddT{Left: numberChannel(1, util.MaxRaw), Right: &healthyRight},
	}})
	tx.Eval(cfg)

	if got := (*tx.Values)[1]; got == center {
		t.Fatalf("setup: expected the healthy tick to write ch2, got center")
	}

	failingRight := []*IOHolder{channelNode(2, nil, &rightRail)}
	*tx.Transmitter.Channels = []*IOHolder{{IO: &InputAdd{
		Id: "add-node", Type: "add",
		Add: AddT{Left: axisChannel(1, "unplugged", &leftRail), Right: &failingRight},
	}}}
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != util.CRSFValue(leftRail) {
		t.Errorf("ch1 got %d, expected its own rail %d", got, leftRail)
	}
	if got := (*tx.Values)[1]; got != util.CRSFValue(rightRail) {
		t.Errorf("ch2 got %d, expected its own rail %d -- every channel the subtree "+
			"can write must be neutralized, not just the one the holder reported",
			got, rightRail)
	}
}

// TestReadIndirectionResolvesTheOwnersRail covers `read`, whose Children() is
// nil: a generic traversal cannot see the node it delegates to, yet its Eval
// returns that node's channel number, so without following the indirection the
// configured rail is lost exactly as in D-2.
func TestReadIndirectionResolvesTheOwnersRail(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	cfg.IOMap["arm-channel"] = axisChannel(5, "unplugged", &rail)

	tx := newTx(&IOHolder{IO: &InputRead{
		Id: "read-node", Type: "read", Read: ReadT{Source: "arm-channel"},
	}})
	tx.Eval(cfg)

	if got := (*tx.Values)[4]; got != util.CRSFValue(offRail) {
		t.Errorf("expected the read target's own rail %d, got %d", offRail, got)
	}
}

// TestReadCycleTerminates guards the one way the node graph can stop being a
// tree: `read` resolves by id, so it can be pointed at itself. The walk is
// depth-bounded rather than cycle-detecting; this fails by hanging, not by
// asserting.
func TestReadCycleTerminates(t *testing.T) {
	cfg := newTestConfig()

	loop := &IOHolder{IO: &InputRead{
		Id: "read-node", Type: "read", Read: ReadT{Source: "loop"},
	}}
	cfg.IOMap["loop"] = loop

	if got := channelOwners(cfg, loop, 0, nil); len(got) != 0 {
		t.Errorf("a read cycle owns no channel, got %d", len(got))
	}
}

// TestNestedChannelIsNotNeutralized pins where the walk stops. A channel node
// discards its child's number, so a channel nested under another one can never
// be written through this holder -- neutralizing it would drive a slot this
// entry does not own.
func TestNestedChannelIsNotNeutralized(t *testing.T) {
	cfg := newTestConfig("unplugged")

	innerRail := util.RawValue(300)
	inner := axisChannel(9, "unplugged", &innerRail)
	outer := channelNode(1, inner, nil)

	tx := newTx(outer)
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != center {
		t.Errorf("ch1 got %d, expected center %d", got, center)
	}
	if got := (*tx.Values)[8]; got != center {
		t.Errorf("ch9 was driven to %d by a nested channel node; the outer channel "+
			"discards the inner number, so this holder does not own ch9", got)
	}
}

// TestWrapperRecoversAfterReattach is the over-correction guard: neutralizing
// must not latch, or the fix trades a frozen stick for a dead one.
func TestWrapperRecoversAfterReattach(t *testing.T) {
	cfg := newTestConfig("unplugged")

	tx := newTx(linearNode(axisChannel(1, "unplugged", nil)))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != center {
		t.Fatalf("setup: expected center while detached, got %d", got)
	}

	*tx.Transmitter.Channels = []*IOHolder{linearNode(numberChannel(1, util.MaxRaw))}
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != full {
		t.Errorf("expected the channel to track live input again (%d), got %d", full, got)
	}
}

// TestHealthyWrapperStillWritesItsValue guards the other direction: the
// neutralizing path must not fire while the subtree resolves normally.
func TestHealthyWrapperStillWritesItsValue(t *testing.T) {
	cfg := newTestConfig()

	rail := offRail
	tx := newTx(linearNode(channelNode(1, &IOHolder{IO: &InputNumber{
		Id: "num-node", Type: "number", Number: NumberT{Output: util.MaxRaw},
	}}, &rail)))

	for tick := 0; tick < 3; tick++ {
		tx.Eval(cfg)
		if got := (*tx.Values)[0]; got != full {
			t.Fatalf("tick %d: a healthy wrapper wrote %d, expected %d "+
				"(the failsafe path must not fire on a resolving subtree)",
				tick, got, full)
		}
	}
}
