// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

// The head-intent enum contract, pinned in Go (review finding MAP-13, owner
// decision D8).
//
// SAFETY BOUNDARY. HeadIntentState is the whole published vocabulary of the
// LOG-ONLY head-intent path: a state above ACTIVE_LOG_ONLY = 8 could only mean
// that head-derived values reach an OUTPUT, which is boundary 1 (no active
// iPhone-derived pan/tilt until a separate reviewed safety milestone) and
// boundary 5 (W3 is LOG-ONLY). The gated unlock is NO-GO/BLOCKED, and this file
// deliberately does not spell its name: .githooks/pre-push check 2 greps *.go
// for that identifier and would refuse every push carrying this comment. The
// name is in FORK-NOTICE.md and the hook header, which are out of that scope.
//
// WHY A TEST AND NOT ONLY THE HOOK. .githooks/pre-push asserts the same shape,
// but it guards PUSHES to the public remote, only in a clone where
// core.hooksPath was set, and `git push --no-verify` bypasses it. This runs on
// every `go test`, on every branch, in every checkout, and it reads the
// GENERATED Go the process actually compiles rather than the .proto the hook
// greps -- so a hand-edited generated file is caught here and nowhere else.
//
// It is deliberately written against pb.HeadIntentState_name rather than the
// typed constants: a constant renamed away would still compile if the test used
// it, whereas the name map is the wire contract in the form the reflection
// service and every client see it.

import (
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
)

// maxHeadIntentState is the ONE number this whole file exists to hold still.
const maxHeadIntentState = int32(8)

// activeLogOnlyName is the only ACTIVE state this fork may publish.
const activeLogOnlyName = "HEAD_INTENT_STATE_ACTIVE_LOG_ONLY"

func TestHeadIntentEnumStopsAtActiveLogOnly(t *testing.T) {
	names := pb.HeadIntentState_name

	var max int32 = -1
	for value := range names {
		if value > max {
			max = value
		}
	}

	if max != maxHeadIntentState {
		t.Fatalf("HeadIntentState reaches %d, want %d. A state above ACTIVE_LOG_ONLY "+
			"can only mean an OUTPUT exists, and the gated unlock is NO-GO/BLOCKED "+
			"(safety boundaries 1 and 5). If the contract really is changing, that "+
			"needs the §2.3.11.6 review, not a passing test", max, maxHeadIntentState)
	}
	if got := names[max]; got != activeLogOnlyName {
		t.Errorf("value %d is named %q, want %q", max, got, activeLogOnlyName)
	}
}

// hasActiveToken reports whether ACTIVE appears as a WHOLE underscore-separated
// token in the name.
//
// A plain substring test is wrong here and the enum says why:
// HEAD_INTENT_STATE_INACTIVE = 6 contains "ACTIVE" and is the exact opposite of
// an active state. The token rule refuses HEAD_INTENT_STATE_ACTIVE and
// HEAD_INTENT_STATE_ACTIVE_OUTPUT while leaving INACTIVE alone.
// .githooks/pre-push check 3 makes the same distinction, by the same rule.
func hasActiveToken(name string) bool {
	for _, token := range strings.Split(name, "_") {
		if token == "ACTIVE" {
			return true
		}
	}
	return false
}

func TestActiveLogOnlyIsTheOnlyActiveState(t *testing.T) {
	for value, name := range pb.HeadIntentState_name {
		if hasActiveToken(name) && name != activeLogOnlyName {
			t.Errorf("HeadIntentState declares %s = %d -- ACTIVE_LOG_ONLY is the only "+
				"ACTIVE state this fork may publish", name, value)
		}
	}
}

// TestTheActiveTokenRuleIsNotASubstringMatch pins the distinction itself, so a
// later tightening to strings.Contains fails here instead of refusing the
// enum's own INACTIVE state (and, in the hook, refusing every clean push).
func TestTheActiveTokenRuleIsNotASubstringMatch(t *testing.T) {
	cases := map[string]bool{
		"HEAD_INTENT_STATE_INACTIVE":      false,
		"HEAD_INTENT_STATE_ACTIVE":        true,
		"HEAD_INTENT_STATE_ACTIVE_OUTPUT": true,
		activeLogOnlyName:                 true,
		"HEAD_INTENT_STATE_NOT_CENTERED":  false,
		"HEAD_INTENT_STATE_REACTIVATED":   false,
	}
	for name, want := range cases {
		if got := hasActiveToken(name); got != want {
			t.Errorf("hasActiveToken(%q) = %v, want %v", name, got, want)
		}
	}
	if _, ok := pb.HeadIntentState_value["HEAD_INTENT_STATE_INACTIVE"]; !ok {
		t.Error("setup: the enum no longer has INACTIVE, which is the state this rule " +
			"exists to keep distinguishable from ACTIVE")
	}
}

// TestHeadIntentEnumIsDenseFromZero closes the gap a "max is 8" check leaves on
// its own: 0..8 each declared exactly once means a value cannot be smuggled in
// by renumbering an existing one.
func TestHeadIntentEnumIsDenseFromZero(t *testing.T) {
	names := pb.HeadIntentState_name

	if len(names) != int(maxHeadIntentState)+1 {
		t.Fatalf("HeadIntentState has %d values, want %d (0..%d)",
			len(names), maxHeadIntentState+1, maxHeadIntentState)
	}
	for value := int32(0); value <= maxHeadIntentState; value++ {
		name, ok := names[value]
		if !ok {
			t.Errorf("no HeadIntentState value %d", value)
			continue
		}
		if got, ok := pb.HeadIntentState_value[name]; !ok || got != value {
			t.Errorf("%s round-trips to %d (present=%v), want %d", name, got, ok, value)
		}
	}
}

// TestTheHookAssertsTheSameThing is a documentation pin, not a behaviour test:
// if this constant ever moves, .githooks/pre-push's check 3 has to move with
// it, and the two live in different languages in different files.
func TestTheHookAssertsTheSameThing(t *testing.T) {
	if maxHeadIntentState != 8 || activeLogOnlyName != "HEAD_INTENT_STATE_ACTIVE_LOG_ONLY" {
		t.Fatal("this file's constants changed -- .githooks/pre-push check 3 hard-codes " +
			"the same two facts (values 0..8, value 8 named ACTIVE_LOG_ONLY) and must " +
			"be updated in the same commit, along with FORK-NOTICE.md")
	}
}
