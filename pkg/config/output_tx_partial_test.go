// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression tests for PARTIAL subtree failure in OutputTransmitter.Eval.
//
// Read the shape of these tests before adding to them, because the shape is the
// whole point of the file. Every test in output_tx_wrapper_test.go detaches the
// WHOLE device. When it does, the left operand of a wrapper goes nan first, the
// holder's result is genuinely unusable, the neutralizing path runs, and the
// suite goes green -- while a subtree in which one channel SURVIVES and another
// DIES sails straight past it. EvalOperation (util.go), the InputAnd/InputOr
// right-operand loops and EvalRelational all IGNORE a nan operand and carry on,
// so the holder reports healthy on a valid channel number and channelOwners was
// never reached at all. Twenty-two rigorous tests missed it for that one reason.
//
// Executed against that fix: add{ch1 <- number, ch2 <- axis on a detached
// gamepad} transmitted ch2 = 1984 -- full deflection -- indefinitely on a healthy
// link, with ch2's configured 172 rail never applied. The `and` variant left a
// detached arm channel at 992, inside a receiver's hysteresis band, so latched
// ON. A single-device config cannot reach any of this: `and` reports allNan and
// the operation family goes nan on its left operand.
//
// So every test here builds a survivor. The survivor is a constant-fed `number`
// channel rather than a second gamepad, for a mechanical reason: devices.
// InputGamepad.Attached() requires a live *sdl.Joystick, and a unit test cannot
// fabricate one -- which is also why the detached side of these tests works, the
// registry entry has a nil handle. What a second real device would add over a
// constant is covered by TestLiveChannelsSurviveWhileAnotherDies below, which
// asserts the neutralizing path leaves resolving channels alone.
//
// The fix walks the subtree BEFORE evaluating it, arming every channel node the
// entry can drive, then reads back which of them the evaluation actually
// resolved. That covers both ways an owner can fail to produce a value --
// evaluated and nan, and never evaluated because an ancestor exited early --
// and neither is answerable from InputChannel.IsNaN, which is only ever a fact
// about the last time that node happened to be evaluated.

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// andNode builds `and` over a right-operand list, with no left argument. The
// operand ORDER matters and is why every caller sets it explicitly: `and`
// reassigns the channel number it reports from each right operand in turn, so
// the number that reaches Eval is whichever operand was evaluated last.
func andNode(right ...*IOHolder) *IOHolder {
	rights := right
	return &IOHolder{IO: &InputAnd{
		Id: "and-node", Type: "and",
		And: AndT{
			Right:       &rights,
			OutputTrue:  rawPtr(util.RawValue(util.CRSFMaxValue)),
			OutputFalse: rawPtr(util.RawValue(util.CRSFMinValue)),
		},
	}}
}

func addNode(left *IOHolder, right ...*IOHolder) *IOHolder {
	rights := right
	return &IOHolder{IO: &InputAdd{
		Id: "add-node", Type: "add",
		Add: AddT{Left: left, Right: &rights},
	}}
}

func gtNode(left *IOHolder, right ...*IOHolder) *IOHolder {
	rights := right
	return &IOHolder{IO: &InputGt{
		Id: "gt-node", Type: "gt",
		Gt: GtT{
			Left: left, Right: &rights,
			OutputTrue:  rawPtr(util.RawValue(util.CRSFMaxValue)),
			OutputFalse: rawPtr(util.RawValue(util.CRSFMinValue)),
		},
	}}
}

// TestOperationHolderReportsHealthyWhileAnOperandIsDead is the defect itself, in
// the exact shape the review reproduced. The holder is not nan and its channel
// number is valid, so every check the previous fix made passes -- and ch2 goes on
// transmitting full deflection from a gamepad that is gone.
func TestOperationHolderReportsHealthyWhileAnOperandIsDead(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	add := addNode(
		numberChannel(1, util.MaxRaw),
		axisChannel(2, "unplugged", &rail),
	)

	// The premise: this is NOT the unusable-result case the earlier fix covers.
	if _, _, ch, nan := add.Eval(cfg); nan || ch != 2 {
		t.Fatalf("premise gone: expected the holder to report healthy on ch2, got ch=%d nan=%v "+
			"(if this changes, this file is testing something else)", ch, nan)
	}

	tx := newTx(add)
	for tick := 0; tick < 5; tick++ {
		tx.Eval(cfg)
	}

	if got := (*tx.Values)[1]; got != util.CRSFValue(offRail) {
		t.Errorf("ch2 = %d after five ticks with its device detached, expected its configured "+
			"rail %d -- a healthy-looking holder must not carry a dead operand's channel",
			got, offRail)
	}
	if got := (*tx.Values)[1]; got == util.CRSFValue(util.CRSFMaxValue) {
		t.Errorf("ch2 is transmitting full deflection from a detached device")
	}
}

// TestLogicalHolderReportsHealthyWhileAnArmChannelIsDead is the arm-safety half:
// `and` left the detached channel at 992, which a receiver's hysteresis HOLDS, so
// a latched arm switch stays latched.
func TestLogicalHolderReportsHealthyWhileAnArmChannelIsDead(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	and := andNode(
		axisChannel(5, "unplugged", &rail),
		numberChannel(1, util.MaxRaw),
	)

	if _, _, ch, nan := and.Eval(cfg); nan || ch != 1 {
		t.Fatalf("premise gone: expected the holder to report healthy on ch1, got ch=%d nan=%v",
			ch, nan)
	}

	tx := newTx(and)
	tx.Eval(cfg)

	if got := (*tx.Values)[4]; got != util.CRSFValue(offRail) {
		t.Errorf("the detached arm channel reads %d, expected its OFF rail %d", got, offRail)
	}
	if got := (*tx.Values)[4]; got == center {
		t.Errorf("center leaves the arm channel latched inside the receiver's hysteresis band")
	}
}

// TestRelationalHolderReportsHealthyWhileAnOperandIsDead covers the comparison
// family, which reaches the same place through EvalRelational -- and note it never
// even propagates the failing operand's number, so nothing about the holder's
// result could have revealed the dead channel.
func TestRelationalHolderReportsHealthyWhileAnOperandIsDead(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	gt := gtNode(
		numberChannel(1, util.MaxRaw),
		axisChannel(2, "unplugged", &rail),
	)

	if _, _, ch, nan := gt.Eval(cfg); nan || ch != 1 {
		t.Fatalf("premise gone: expected the holder to report healthy on the LEFT channel, "+
			"got ch=%d nan=%v", ch, nan)
	}

	tx := newTx(gt)
	tx.Eval(cfg)

	if got := (*tx.Values)[1]; got != util.CRSFValue(offRail) {
		t.Errorf("ch2 = %d, expected its configured rail %d", got, offRail)
	}
}

// TestLiveChannelsSurviveWhileAnotherDies is the collateral-damage guard, and it
// is what the "one survives, another dies" requirement is really for: the
// neutralizing path is per-owner, so it must rail the dead channel and leave the
// resolving ones tracking their inputs.
func TestLiveChannelsSurviveWhileAnotherDies(t *testing.T) {
	cfg := newTestConfig("unplugged")

	deadRail := offRail
	liveA, liveB := util.RawValue(200), util.RawValue(300)

	add := addNode(
		channelNode(1, numberInput(util.MaxRaw), &liveA),
		axisChannel(2, "unplugged", &deadRail),
		channelNode(3, numberInput(util.MaxRaw), &liveB),
	)

	if _, _, _, nan := add.Eval(cfg); nan {
		t.Fatalf("premise gone: with resolving operands the holder must report healthy")
	}

	tx := newTx(add)
	tx.Eval(cfg)

	if got := (*tx.Values)[1]; got != util.CRSFValue(deadRail) {
		t.Errorf("the detached ch2 reads %d, expected its rail %d", got, deadRail)
	}
	if got := (*tx.Values)[0]; got == util.CRSFValue(liveA) {
		t.Errorf("ch1 was driven to its failsafe rail %d, but its input resolves -- "+
			"neutralizing a dead operand must not take live channels with it", liveA)
	}
	if got := (*tx.Values)[2]; got == util.CRSFValue(liveB) {
		t.Errorf("ch3 was driven to its failsafe rail %d, but its input resolves", liveB)
	}
}

// TestEarlyExitOperandIsNeutralized is the case that decides how the per-owner
// state has to be derived, and it is the reason the reviewer's interim patch was
// not shipped.
//
// `and` exits the moment a right operand reads falsy, so every operand after it
// is never evaluated at all this tick. Its IsNaN therefore says whatever the last
// tick that DID reach it left behind -- here, false, from a healthy evaluation --
// so a fix that reads IsNaN concludes the channel is fine and leaves the slot
// holding. Only a marker armed by the walk and cleared by the evaluation can tell
// "resolved" from "never asked".
//
// The slot must rail: nothing is producing a value for it this tick, and
// hold-last is the defect this whole chain exists to remove. The operand would
// resolve if it were asked, which is exactly why IsNaN cannot see the problem.
func TestEarlyExitOperandIsNeutralized(t *testing.T) {
	cfg := newTestConfig()

	rail := offRail
	skipped := channelNode(3, numberInput(util.MaxRaw), &rail)
	skippedNode := skipped.IO.(*InputChannel)

	// Give it a healthy history, so a stale read has something wrong to find.
	if _, _, _, nan := skipped.Eval(cfg); nan {
		t.Fatalf("setup: expected a constant-fed operand to resolve")
	}

	// Falsy first, so `and` returns before it ever reaches the operand above. A
	// channel maps MinRaw onto CRSF 0, which is the falsy reading.
	and := andNode(numberChannel(1, util.MinRaw), skipped)

	tx := newTx(and)
	tx.Eval(cfg)

	if skippedNode.IsNaN {
		t.Fatalf("premise gone: the skipped operand's IsNaN is true, so this test no longer "+
			"distinguishes a stale read from a fresh one (evalPass=%v)", skippedNode.evalPass)
	}
	if skippedNode.resolvedThisPass() {
		t.Errorf("an operand the evaluation never reached reported itself resolved")
	}

	if got := (*tx.Values)[2]; got != util.CRSFValue(offRail) {
		t.Errorf("ch3 = %d, expected its rail %d -- an operand skipped by an early exit is "+
			"not producing a value, so its slot must not hold one", got, offRail)
	}
}

// TestStaleEvaluationStateIsNotReadAsResolved pins the mechanism on its own, so a
// future change that goes back to reading IsNaN fails here rather than through one
// of its downstream effects.
func TestStaleEvaluationStateIsNotReadAsResolved(t *testing.T) {
	cfg := newTestConfig()

	ch := numberChannel(1, util.MaxRaw)
	node := ch.IO.(*InputChannel)

	if _, _, _, nan := ch.Eval(cfg); nan {
		t.Fatalf("setup: expected a constant-fed channel to resolve")
	}
	if !node.resolvedThisPass() {
		t.Fatalf("setup: expected the evaluated node to report resolved")
	}

	// What the walk does at the start of every pass.
	node.armEvalPass()

	if node.resolvedThisPass() {
		t.Errorf("a node that has been armed and not yet evaluated reported resolved; " +
			"the marker is carrying state across passes")
	}
	if node.IsNaN {
		t.Errorf("premise gone: IsNaN should still read false here. It is a fact about the " +
			"last evaluation, not about this pass, and that gap is the whole point")
	}
}

// TestHealthyPartialSubtreeIsNotNeutralized is the over-correction guard, and it
// matters more than usual here: a spurious neutral on a live channel would put a
// value on the control path that nothing commanded.
func TestHealthyPartialSubtreeIsNotNeutralized(t *testing.T) {
	cfg := newTestConfig()

	rail := offRail
	add := addNode(
		channelNode(1, numberInput(util.MaxRaw), &rail),
		channelNode(2, numberInput(util.MaxRaw), &rail),
	)

	tx := newTx(add)
	for tick := 0; tick < 5; tick++ {
		tx.Eval(cfg)

		if got := (*tx.Values)[1]; got == util.CRSFValue(offRail) {
			t.Fatalf("tick %d: ch2 fell to its failsafe rail while every operand resolved", tick)
		}
	}
}

// TestPartialFailureRecoversOnReattach checks the neutral does not latch on this
// path either: the fix must not trade a stale channel for a dead one.
func TestPartialFailureRecoversOnReattach(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	tx := newTx(addNode(
		numberChannel(1, util.MaxRaw),
		axisChannel(2, "unplugged", &rail),
	))
	tx.Eval(cfg)

	if got := (*tx.Values)[1]; got != util.CRSFValue(offRail) {
		t.Fatalf("setup: expected ch2 at its rail while detached, got %d", got)
	}

	*tx.Transmitter.Channels = []*IOHolder{addNode(
		numberChannel(1, util.MaxRaw),
		numberChannel(2, util.MaxRaw),
	)}
	tx.Eval(cfg)

	if got := (*tx.Values)[1]; got == util.CRSFValue(offRail) {
		t.Errorf("ch2 stayed at its failsafe rail after its input resolved again")
	}
}

// TestOpaqueTopLevelTypePinsItsChannelToTheRail pins the behaviour of the seven
// always--1 types, which no test covered.
//
// They report ch = -1 on BOTH paths, so a top-level `invert` is unusable to Eval
// on every tick, healthy or not, and the channel under it is driven to its
// failsafe every tick. That is the safe direction and an improvement on the 992
// the slot sat at before -- but it is behaviour, so it is pinned rather than
// assumed. The wrapper test file's line about these types ("neither can strand a
// slot") is a statement about the OLD defect and is not a reason to leave them
// untested. If a future change makes them propagate a channel number, this test
// is where it shows up.
func TestOpaqueTopLevelTypePinsItsChannelToTheRail(t *testing.T) {
	cfg := newTestConfig()

	rail := offRail
	invert := &IOHolder{IO: &InputInvert{
		Id: "inv-node", Type: "invert",
		Invert: InvertT{Input: channelNode(7, numberInput(util.MaxRaw), &rail)},
	}}

	if _, _, ch, nan := invert.Eval(cfg); nan || ch != -1 {
		t.Fatalf("premise gone: expected a healthy invert to report ch=-1 without nan, "+
			"got ch=%d nan=%v", ch, nan)
	}

	tx := newTx(invert)
	for tick := 0; tick < 3; tick++ {
		tx.Eval(cfg)
		if got := (*tx.Values)[6]; got != util.CRSFValue(offRail) {
			t.Errorf("tick %d: ch7 = %d, expected the owner's rail %d", tick, got, offRail)
		}
	}
}

// TestOpaqueTopLevelTypeWithNoChannelIsANoOp is the other half, and it is the
// distinction the truncation branch depends on: an entry that legitimately owns
// no channel writes nothing at all and suppresses nothing. Only an entry whose
// owners are UNKNOWN suppresses. See output_tx_depth_test.go.
func TestOpaqueTopLevelTypeWithNoChannelIsANoOp(t *testing.T) {
	cfg := newTestConfig("unplugged")

	deadzone := util.ZeroRaw
	axis := &IOHolder{IO: &InputAxis{
		Id: "axis-node", Type: "axis",
		Axis: AxisT{Input: &IOHolder{IO: &InputGamepad{
			Id: "gp-node", Type: "gamepad", Gamepad: GamepadT{Id: "unplugged", Name: "test"},
		}}, Number: 0, Deadzone: &deadzone},
	}}

	tx := newTx(axis)
	tx.Eval(cfg)

	for slot, got := range *tx.Values {
		if got != center {
			t.Errorf("ch%d = %d: an entry that owns no channel must write nothing", slot+1, got)
		}
	}
	if tx.Unresolved.Load() {
		t.Errorf("owning no channel is not the same as not knowing which channels are owned; " +
			"only the latter may suppress")
	}
}
