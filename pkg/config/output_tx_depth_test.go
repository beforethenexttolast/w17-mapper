// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression tests for the depth bound on the channel-owner walk.
//
// The defect: channelOwnerMaxDepth was 32, and a tree deeper than that truncated
// to ZERO owners. Eval then had nothing to neutralize, the entry's reported
// channel was -1, the `ch >= 1` guard wrote nothing, and the slot kept its last
// value -- hold-last, silently, on the very path built to remove it. Reproduced
// with a 40-deep `linear` chain: ch1 = 1984 after five detached ticks.
//
// Two halves to the fix, and they are independent:
//
//  1. The bound is 256, so it effectively never fires. 40 nested wrappers was
//     already enough to trip 32, which is not a comfortable margin for a value
//     nothing enforces at config-entry time.
//  2. Truncation is FAIL-SAFE regardless of the bound. A truncated walk means the
//     owner set is incomplete, so which channels the entry drives is unknown, and
//     unknown state on this path is answered by transmitting nothing -- the same
//     answer 630ea96 gives for no-config and configSwapGate gives for a swap.
//     Which is not the same as an entry that legitimately owns NO channel: that
//     one drives no slot, so there is nothing to neutralize and nothing to
//     suppress. TestOpaqueTopLevelTypeWithNoChannelIsANoOp holds that line.
//
// The bound is NOT a `read`-cycle backstop, whatever the comment on it used to
// claim, and no test here evaluates a cyclic config: InputRead._Eval recurses
// through Config.IOMap unguarded and takes the process down with a stack overflow
// before the walk is reached. That is a pre-existing upstream defect (present at
// 2b8031a), tracked separately and deliberately not fixed here. What the walk can
// be shown to do on a cycle is terminate, and that is
// TestChannelOwnersWalkTerminatesOnAReadCycle in output_tx_wrapper_test.go.

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// nested wraps a leaf in `depth` identity `linear` nodes.
func nested(leaf *IOHolder, depth int) *IOHolder {
	node := leaf
	for i := 0; i < depth; i++ {
		node = linearNode(node)
	}
	return node
}

// TestDeepTreeStillNeutralizes is the reproduction, at the depth that used to
// break it. With the bound raised, the walk reaches the owner and the channel
// neutralizes normally -- no suppression, because nothing is unknown.
func TestDeepTreeStillNeutralizes(t *testing.T) {
	cfg := newTestConfig("unplugged")

	tx := newTx(nested(numberChannel(1, util.MaxRaw), 40))
	tx.Eval(cfg)

	if got := (*tx.Values)[0]; got != util.CRSFValue(util.CRSFMaxValue) {
		t.Fatalf("setup: expected ch1 at full deflection after a healthy tick, got %d", got)
	}

	*tx.Transmitter.Channels = []*IOHolder{nested(axisChannel(1, "unplugged", nil), 40)}
	for tick := 0; tick < 5; tick++ {
		tx.Eval(cfg)
		if got := (*tx.Values)[0]; got != center {
			t.Fatalf("tick %d: ch1 stranded at %d through a 40-deep tree, expected center %d",
				tick, got, center)
		}
	}

	if tx.Unresolved.Load() {
		t.Errorf("a tree the walk can traverse fully must not suppress")
	}
}

// TestBoundIsFarAboveAnythingTheEditorCanBuild states the sizing intent as an
// assertion, so lowering the bound back toward the depth of a plausible config is
// a test failure and not a silent regression.
func TestBoundIsFarAboveAnythingTheEditorCanBuild(t *testing.T) {
	const deepestPlausibleConfig = 64

	if channelOwnerMaxDepth < 4*deepestPlausibleConfig {
		t.Errorf("channelOwnerMaxDepth = %d leaves too little margin over a plausible "+
			"config depth of %d; the bound is meant never to fire in practice, and when it "+
			"does fire the port stops transmitting",
			channelOwnerMaxDepth, deepestPlausibleConfig)
	}

	cfg := newTestConfig("unplugged")
	owners, truncated := channelOwners(cfg, nested(axisChannel(1, "unplugged", nil),
		deepestPlausibleConfig))

	if truncated {
		t.Errorf("a %d-deep config truncated the walk", deepestPlausibleConfig)
	}
	if len(owners) != 1 {
		t.Errorf("expected the walk to reach the one channel at the bottom, got %d owners",
			len(owners))
	}
}

// TestTruncatedWalkSuppressesRatherThanHolding is the second half, and it is the
// one that matters if the bound is ever wrong. Past the bound the walk cannot
// report the owner, so nothing can neutralize the slot -- and the answer is to
// stop transmitting the port, not to transmit a value nothing accounts for.
//
// The stranded value is reached the only way it can be: a config that was
// shallow and live is replaced by one deep enough to truncate. A config that
// truncates from the start never writes a live value in the first place, because
// truncation makes its result unusable on every tick, healthy included -- which
// is TestTruncatedEntryDoesNotWriteAResultItCannotAccountFor below.
func TestTruncatedWalkSuppressesRatherThanHolding(t *testing.T) {
	cfg := newTestConfig("unplugged")

	tooDeep := channelOwnerMaxDepth + 8

	owners, truncated := channelOwners(cfg, nested(axisChannel(1, "unplugged", nil), tooDeep))
	if !truncated {
		t.Fatalf("setup: expected a %d-deep tree to truncate the walk", tooDeep)
	}
	if len(owners) != 0 {
		t.Fatalf("setup: expected the truncated walk to reach no owner, got %d", len(owners))
	}

	tx := newTx(numberChannel(1, util.MaxRaw))
	tx.Eval(cfg)
	if got := (*tx.Values)[0]; got != util.CRSFValue(util.CRSFMaxValue) {
		t.Fatalf("setup: expected ch1 at full deflection, got %d", got)
	}

	*tx.Transmitter.Channels = []*IOHolder{nested(axisChannel(1, "unplugged", nil), tooDeep)}
	tx.Eval(cfg)

	if !tx.Unresolved.Load() {
		t.Errorf("a truncated walk left the transmitter marked resolved, so the send loop " +
			"would go on transmitting an array with a channel nothing can account for")
	}

	// Stated rather than asserted away: the slot really is still holding, and
	// nothing in Eval can fix that -- the owner is past the bound, so its
	// failsafe is not reachable. Suppression is the whole of the protection here.
	if got := (*tx.Values)[0]; got == util.CRSFValue(util.CRSFMaxValue) {
		t.Logf("ch1 still reads %d, as expected: an owner past the depth bound cannot be "+
			"neutralized, which is exactly why the port stops transmitting instead", got)
	}
}

// TestSuppressionClearsWhenTheWalkCompletesAgain is the over-correction guard.
// Suppression on this path is a statement about the current config, not a latch:
// applying a config the walk can traverse must resume transmission.
func TestSuppressionClearsWhenTheWalkCompletesAgain(t *testing.T) {
	cfg := newTestConfig("unplugged")

	tx := newTx(nested(axisChannel(1, "unplugged", nil), channelOwnerMaxDepth+8))
	tx.Eval(cfg)

	if !tx.Unresolved.Load() {
		t.Fatalf("setup: expected the deep config to suppress")
	}

	*tx.Transmitter.Channels = []*IOHolder{axisChannel(1, "unplugged", nil)}
	tx.Eval(cfg)

	if tx.Unresolved.Load() {
		t.Errorf("suppression latched: a config the walk traverses fully must transmit again")
	}
}

// BenchmarkEvalWalkCost bounds what the walk adds to the send path. It now runs
// on EVERY tick for every top-level entry, not just on the failing path, so the
// cost is worth a number rather than an assurance: this is a 16-channel config of
// plain channel nodes, the shape the UI builds, evaluated once per CRSF frame
// (~4 ms). Measured on this host at ~381 ns per Eval for all 16 channels, which
// is about 0.01% of a frame interval. The walk is a pointer traversal that
// evaluates no nodes, so it stays proportional to config size, not to input rate.
func BenchmarkEvalWalkCost(b *testing.B) {
	cfg := newTestConfig()

	channels := make([]*IOHolder, 0, 16)
	for n := int32(1); n <= 16; n++ {
		channels = append(channels, numberChannel(n, util.MaxRaw))
	}
	tx := newTx(channels...)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx.Eval(cfg)
	}
}

// TestTruncatedEntryDoesNotWriteAResultItCannotAccountFor pins the other half of
// the truncation branch. Truncation forces the entry's result to be treated as
// unusable even when the subtree evaluates perfectly well, so a config that
// truncates from the start never puts a live value in the slot at all -- there is
// nothing for a later tick to strand.
//
// It also checks the blast radius: suppression is per port, but neutralization is
// still per entry, so the entries the walk CAN account for keep being driven to
// their rails while another entry is suppressed. That is what makes the array
// safe to resume from.
func TestTruncatedEntryDoesNotWriteAResultItCannotAccountFor(t *testing.T) {
	cfg := newTestConfig("unplugged")

	rail := offRail
	tooDeep := channelOwnerMaxDepth + 8

	// ch1: deep, and its subtree is HEALTHY -- full deflection, if it were
	// written. ch2: shallow, detached, so it must reach its rail.
	tx := newTx(
		nested(numberChannel(1, util.MaxRaw), tooDeep),
		axisChannel(2, "unplugged", &rail),
	)
	tx.Eval(cfg)

	if !tx.Unresolved.Load() {
		t.Fatalf("setup: expected the deep entry to suppress the port")
	}
	if got := (*tx.Values)[0]; got == util.CRSFValue(util.CRSFMaxValue) {
		t.Errorf("ch1 = %d: a truncated entry wrote the value it computed, which is a value "+
			"the code cannot account for -- and it would be carried by the first frame after "+
			"suppression clears", got)
	}
	if got := (*tx.Values)[1]; got != util.CRSFValue(offRail) {
		t.Errorf("ch2 = %d, expected its rail %d: entries the walk can account for are still "+
			"neutralized while another entry on the same port is suppressed", got, offRail)
	}
}
