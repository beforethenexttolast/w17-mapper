// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression tests for the silent re-arm on reconnect (adversarial review of
// the 2026-08-16 wave, blocker F2).
//
// The gap: a seq used as an arm toggle HOLDS its state through a gamepad
// dropout -- its allNan branch returns the current output as healthy. The
// profile's and-gate rails ch5 to 172 while the pad is DEAD, but the moment a
// controller auto-reconnects, the still-armed toggle puts ch5 back at 1811
// with zero user input, and the firmware ArmGate (which has no reconnect
// policy) arms at neutral throttle.
//
// The fix under test: SeqT.ResetOnNaN (opt-in). Any all-nan episode returns
// the sequence to output_values[0] and demands a FRESH press -- a full
// release-then-press observed after recovery -- before the next activation,
// so a press that spanned the dropout cannot register as a toggle either.

import (
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// settableInput is a condition whose value and liveness the test scripts
// directly between Eval calls (single goroutine, no synchronization needed).
type settableInput struct {
	value util.RawValue
	dead  bool
}

func (s *settableInput) Eval(*Config) (IOType, util.RawValue, util.ChannelNumber, bool) {
	if s.dead {
		return nil, 0, -1, true
	}
	return nil, s.value, -1, false
}
func (s *settableInput) InputType() string          { return "test-settable" }
func (s *settableInput) InputValue() *util.RawValue { return nil }
func (s *settableInput) InputId() string            { return "settable" }
func (s *settableInput) Children() *[]*IOHolder     { return nil }

// newToggleSeq builds a two-value toggle seq driven by the given condition.
// The activation window is [0, forever] so tests need no sleeps; the length-2
// special case still requires an explicit traversal method (a nil method
// never advances -- pinned in the profile tests).
func newToggleSeq(cond *settableInput, resetOnNan bool) *InputSeq {
	values := []util.RawValue{0, 32767}
	method := ClockwiseTraversal
	conditions := []*IOHolder{{IO: cond}}
	return &InputSeq{
		Id: "toggle", Type: "seq",
		Seq: SeqT{
			Conditions:            &conditions,
			OutputValues:          &values,
			TraversalMethod:       &method,
			ActivationDurationMin: 0,
			ActivationDurationMax: 1 << 30,
			ResetOnNaN:            resetOnNan,
			currentDirection:      ForwardDirection,
		},
	}
}

func evalSeq(t *testing.T, seq *InputSeq, cfg *Config) util.RawValue {
	t.Helper()
	_, out, _, nan := seq.Eval(cfg)
	if nan {
		t.Fatalf("the toggle seq itself should never be nan, got nan")
	}
	return out
}

// press-and-release toggles the seq once.
func pressRelease(t *testing.T, seq *InputSeq, cond *settableInput, cfg *Config) {
	t.Helper()
	cond.value = 32767
	evalSeq(t, seq, cfg)
	cond.value = 0
	evalSeq(t, seq, cfg)
}

// TestSeqLegacyHoldsThroughDropout pins the upstream behaviour that made the
// flag necessary: WITHOUT reset_on_nan, a dropout leaves the toggle armed and
// a reconnect resumes transmitting it -- the silent re-arm.
func TestSeqLegacyHoldsThroughDropout(t *testing.T) {
	cfg := newTestConfig()
	cond := &settableInput{}
	seq := newToggleSeq(cond, false)

	pressRelease(t, seq, cond, cfg)
	if got := evalSeq(t, seq, cfg); got != 32767 {
		t.Fatalf("setup: expected the toggle armed, got %d", got)
	}

	cond.dead = true
	if got := evalSeq(t, seq, cfg); got != 32767 {
		t.Fatalf("upstream allNan behaviour changed: expected the held 32767, got %d "+
			"(if seq now resets by default, the opt-in contract is broken)", got)
	}

	cond.dead = false
	cond.value = 0
	if got := evalSeq(t, seq, cfg); got != 32767 {
		t.Fatalf("expected the legacy toggle still armed after reconnect, got %d", got)
	}
}

// TestSeqResetOnNanDisarmsUntilFreshPress is the F2 fix at the seq level: the
// dropout itself resets the toggle, the reconnect does NOT re-arm it, and only
// a fresh deliberate press does.
func TestSeqResetOnNanDisarmsUntilFreshPress(t *testing.T) {
	cfg := newTestConfig()
	cond := &settableInput{}
	seq := newToggleSeq(cond, true)

	pressRelease(t, seq, cond, cfg)
	if got := evalSeq(t, seq, cfg); got != 32767 {
		t.Fatalf("setup: expected the toggle armed, got %d", got)
	}

	// Dropout: the episode itself resets the toggle to output_values[0].
	cond.dead = true
	for tick := 0; tick < 3; tick++ {
		if got := evalSeq(t, seq, cfg); got != 0 {
			t.Fatalf("tick %d: expected the toggle reset to 0 during the dropout, got %d",
				tick, got)
		}
	}

	// Auto-reconnect with the button released: still disarmed, however long
	// it sits there.
	cond.dead = false
	cond.value = 0
	for tick := 0; tick < 3; tick++ {
		if got := evalSeq(t, seq, cfg); got != 0 {
			t.Fatalf("tick %d after reconnect: expected 0 with no user input, got %d",
				tick, got)
		}
	}

	// A fresh deliberate press re-arms.
	pressRelease(t, seq, cond, cfg)
	if got := evalSeq(t, seq, cfg); got != 32767 {
		t.Errorf("expected a fresh press to arm again, got %d", got)
	}
}

// TestSeqResetOnNanPressSpanningDropout: a press that started before the
// signal died and was still held on recovery must NOT count as a toggle --
// only a release-then-press observed entirely after recovery may.
func TestSeqResetOnNanPressSpanningDropout(t *testing.T) {
	cfg := newTestConfig()
	cond := &settableInput{}
	seq := newToggleSeq(cond, true)

	// Disarmed; the press begins...
	cond.value = 32767
	if got := evalSeq(t, seq, cfg); got != 0 {
		t.Fatalf("a held press must not toggle by itself, got %d", got)
	}

	// ...the pad dies mid-press...
	cond.dead = true
	if got := evalSeq(t, seq, cfg); got != 0 {
		t.Fatalf("expected 0 during the dropout, got %d", got)
	}

	// ...and comes back with the button STILL held.
	cond.dead = false
	cond.value = 32767
	if got := evalSeq(t, seq, cfg); got != 0 {
		t.Fatalf("a press spanning the dropout must not arm on reconnect, got %d", got)
	}

	// Releasing it must not toggle either: the activation edge was discarded.
	cond.value = 0
	if got := evalSeq(t, seq, cfg); got != 0 {
		t.Errorf("releasing a dropout-spanning press must not register a toggle, got %d", got)
	}

	// The NEXT full press+release is fresh and toggles.
	pressRelease(t, seq, cond, cfg)
	if got := evalSeq(t, seq, cfg); got != 32767 {
		t.Errorf("expected the first clean press after recovery to arm, got %d", got)
	}
}

// TestArmChainDropoutReconnectStaysAtRail is F2 end to end at the channel
// level, in the exact shape the committed profile uses:
//
//	ch5 = and(seq{reset_on_nan}, [liveness]) -> 172/1811 anchors
//
// Dropout rails ch5 to 172; the reconnect leaves it at 172 with no user
// input; a fresh TRIANGLE press-and-release arms it again.
func TestArmChainDropoutReconnectStaysAtRail(t *testing.T) {
	cfg := newTestConfig()

	button := &settableInput{}
	probe := &settableInput{value: 1} // the liveness probe: constant 1 while alive

	seq := newToggleSeq(button, true)
	outFalse, outTrue := util.RawValue(-32768), util.RawValue(32767)
	right := []*IOHolder{{IO: probe}}
	gate := &IOHolder{IO: &InputAnd{
		Id: "arm-gate", Type: "and",
		And: AndT{
			OutputFalse: &outFalse, OutputTrue: &outTrue,
			Left:  &IOHolder{IO: seq},
			Right: &right,
		},
	}}

	rail := offRail
	tx := newTx(channelNode(5, gate, &rail))

	crsfOf := func() util.CRSFValue {
		t.Helper()
		tx.Eval(cfg)
		return (*tx.Values)[4]
	}

	const armed = util.CRSFValue(1811)
	const disarmedRail = util.CRSFValue(172)

	// Note on anchors: channelNode maps raw [-32768, 32767] onto the schema
	// endpoints; this test needs the 172/1811 anchors the profile uses.
	crsfMin, crsfMax := util.RawValue(172), util.RawValue(1811)
	ch := tx.Transmitter.Channels
	(*ch)[0].IO.(*InputChannel).Channel.CRSFMin = &crsfMin
	(*ch)[0].IO.(*InputChannel).Channel.CRSFMax = &crsfMax

	// Boot: disarmed at the rail.
	if got := crsfOf(); got != disarmedRail {
		t.Fatalf("boot: expected ch5 at the %d rail, got %d", disarmedRail, got)
	}

	// Deliberate press+release arms.
	button.value = 32767
	crsfOf()
	button.value = 0
	if got := crsfOf(); got != armed {
		t.Fatalf("expected ch5 armed at %d, got %d", armed, got)
	}

	// The pad dies: everything it feeds dies together.
	button.dead, probe.dead = true, true
	if got := crsfOf(); got != disarmedRail {
		t.Fatalf("dropout: expected ch5 at the %d rail, got %d", disarmedRail, got)
	}

	// Auto-reconnect, no user input: MUST stay at the rail. This is the
	// silent re-arm the blocker names -- before the fix, ch5 returned to
	// 1811 here and the firmware armed at neutral throttle.
	button.dead, probe.dead = false, false
	button.value = 0
	for tick := 0; tick < 3; tick++ {
		if got := crsfOf(); got != disarmedRail {
			t.Fatalf("tick %d after reconnect: ch5 re-armed to %d with no user input",
				tick, got)
		}
	}

	// A fresh deliberate press arms again.
	button.value = 32767
	crsfOf()
	button.value = 0
	if got := crsfOf(); got != armed {
		t.Errorf("expected a fresh press to arm ch5 at %d, got %d", armed, got)
	}
}
