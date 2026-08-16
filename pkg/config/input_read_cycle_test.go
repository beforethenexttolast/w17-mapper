// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Regression tests for the unguarded InputRead recursion.
//
// The defect: InputRead._Eval followed Config.IOMap with no guard, so a
// schema-valid pair of `read` nodes pointing at each other recursed until the
// stack was exhausted and killed the WHOLE process (empirically demonstrated
// during the 2026-08-16 audit; a Go stack overflow is not recoverable, which is
// why the "before" behaviour cannot be demonstrated inside this suite). The
// process-level consequence was a receiver link-loss failsafe -- a stop, not
// runaway output -- but a crash is not a designed failure mode.
//
// The fix has two layers, both covered here:
//   1. A runtime re-entrancy guard in InputRead._Eval: a cycle evaluates to
//      nan, which the existing failsafe machinery turns into an inert,
//      neutralized channel.
//   2. A load-time check, Config.CheckReadCycles, wired into the server's
//      SetConfig: a cyclic config is refused before it is applied, with the
//      loop spelled out.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// readNode builds a top-level `read` entry.
func readNode(id, source string) *IOHolder {
	return &IOHolder{IO: &InputRead{
		Id: id, Type: "read", Read: ReadT{Source: source},
	}}
}

// TestReadCycleEvaluatesToNaN is the core regression: evaluating a two-entry
// read loop must return (and terminate at all), not overflow the stack.
func TestReadCycleEvaluatesToNaN(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["a"] = readNode("a", "b")
	cfg.IOMap["b"] = readNode("b", "a")

	for tick := 0; tick < 3; tick++ {
		_, _, _, nan := cfg.IOMap["a"].Eval(cfg)
		if !nan {
			t.Fatalf("tick %d: a read cycle must evaluate to nan", tick)
		}
	}
}

// TestReadSelfCycleEvaluatesToNaN covers the smallest loop: an entry reading
// itself.
func TestReadSelfCycleEvaluatesToNaN(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["self"] = readNode("self", "self")

	if _, _, _, nan := cfg.IOMap["self"].Eval(cfg); !nan {
		t.Fatal("a self-read must evaluate to nan")
	}
}

// TestReadCycleChannelFailsSafe pins what a cycle does to a transmitter when
// the channel node itself is intact: the owner walk stops AT the channel (so
// every channel is accounted for and nothing needs suppressing), the cyclic
// input evaluates to nan through the guard, and the channel is driven to its
// configured failsafe. An inert channel, exactly as designed.
func TestReadCycleChannelFailsSafe(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["loop"] = readNode("loop", "loop")

	rail := offRail
	tx := newTx(channelNode(5, readNode("in-tree-read", "loop"), &rail))
	tx.Eval(cfg)

	if got := (*tx.Values)[4]; got != util.CRSFValue(offRail) {
		t.Errorf("expected the cyclic channel at its OFF rail %d, got %d", offRail, got)
	}
	if tx.Unresolved.Load() {
		t.Errorf("owners were all found and neutralized; the port must not be " +
			"flagged unresolved for an accounted-for failure")
	}
}

// TestReadCycleWithoutOwnersSuppresses covers the other transmitter shape: the
// loop sits ABOVE any channel node, so the owner walk chases it through IOMap
// until its depth bound truncates. An incomplete owner set means the entry's
// channels are unknown, and the existing fail-safe answer to unknown state
// fires: the transmitter is flagged unresolved and the send loop suppresses
// the port's frames.
func TestReadCycleWithoutOwnersSuppresses(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["x"] = readNode("x", "y")
	cfg.IOMap["y"] = readNode("y", "x")

	tx := newTx(readNode("entry", "x"))
	tx.Eval(cfg)

	if !tx.Unresolved.Load() {
		t.Errorf("expected the truncated owner walk to flag the port unresolved")
	}
}

// TestReadDiamondIsNotACycle guards against false positives: two entries
// reading the SAME third entry share a node without forming a loop, and the
// value must flow through both -- re-evaluating a read sequentially is normal,
// only re-entering it is a cycle.
func TestReadDiamondIsNotACycle(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["src"] = numberInput(util.MaxRaw)
	cfg.IOMap["left"] = readNode("left", "src")
	cfg.IOMap["right"] = readNode("right", "src")

	if err := cfg.CheckReadCycles(); err != nil {
		t.Fatalf("a diamond is not a cycle, got: %v", err)
	}

	for _, name := range []string{"left", "right"} {
		_, out, _, nan := cfg.IOMap[name].Eval(cfg)
		if nan || out != util.MaxRaw {
			t.Errorf("%s: expected %d flowing through the diamond, got out=%d nan=%v",
				name, util.MaxRaw, out, nan)
		}
	}
}

// TestLongReadChainStillEvaluates guards the guard: re-entrancy detection has
// no depth limit, so a deep but acyclic chain keeps working.
func TestLongReadChainStillEvaluates(t *testing.T) {
	cfg := newTestConfig()
	cfg.IOMap["end"] = numberInput(util.MaxRaw)
	prev := "end"
	for i := 0; i < 40; i++ {
		name := "hop" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		cfg.IOMap[name] = readNode(name, prev)
		prev = name
	}

	if err := cfg.CheckReadCycles(); err != nil {
		t.Fatalf("an acyclic chain must pass the load-time check, got: %v", err)
	}
	if _, out, _, nan := cfg.IOMap[prev].Eval(cfg); nan || out != util.MaxRaw {
		t.Errorf("expected %d through the chain, got out=%d nan=%v", util.MaxRaw, out, nan)
	}
}

// TestCheckReadCyclesDetects covers the load-time refusal for the shapes that
// matter: a two-entry loop, a self-loop, a loop buried under wrapper nodes, and
// the non-errors (dangling source, nil config).
func TestCheckReadCyclesDetects(t *testing.T) {
	t.Run("two-entry loop", func(t *testing.T) {
		cfg := newTestConfig()
		cfg.IOMap["a"] = readNode("a", "b")
		cfg.IOMap["b"] = readNode("b", "a")

		err := cfg.CheckReadCycles()
		if err == nil {
			t.Fatal("expected a cycle error")
		}
		for _, name := range []string{"a", "b"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the error should spell out the loop; %q missing from %q", name, err)
			}
		}
	})

	t.Run("self loop", func(t *testing.T) {
		cfg := newTestConfig()
		cfg.IOMap["self"] = readNode("self", "self")
		if cfg.CheckReadCycles() == nil {
			t.Fatal("expected a cycle error for a self-read")
		}
	})

	t.Run("loop under a channel", func(t *testing.T) {
		// The read closing the loop sits inside a channel's subtree, not at the
		// top level -- the walk must find it through Children().
		cfg := newTestConfig()
		cfg.IOMap["loop"] = readNode("loop", "entry")
		cfg.IOMap["entry"] = channelNode(1, readNode("nested", "loop"), nil)
		if cfg.CheckReadCycles() == nil {
			t.Fatal("expected a cycle error for a loop routed through a channel subtree")
		}
	})

	t.Run("dangling source is not a cycle", func(t *testing.T) {
		cfg := newTestConfig()
		cfg.IOMap["a"] = readNode("a", "missing")
		if err := cfg.CheckReadCycles(); err != nil {
			t.Fatalf("a dangling source evaluates to nan and is not a cycle, got: %v", err)
		}
	})

	t.Run("nil map and nil config", func(t *testing.T) {
		if err := (&Config{}).CheckReadCycles(); err != nil {
			t.Fatalf("nil IOMap: %v", err)
		}
		if err := (*Config)(nil).CheckReadCycles(); err != nil {
			t.Fatalf("nil config: %v", err)
		}
	})
}

// TestSchemaValidCycleIsCaughtAtLoad closes the end-to-end claim: the cyclic
// config PASSES schema validation (that is what made the defect reachable from
// the editor and from -config-file-path), and the load path must therefore
// catch it separately. This walks the same validate -> unmarshal -> cycle-check
// sequence the server's SetConfig performs.
func TestSchemaValidCycleIsCaughtAtLoad(t *testing.T) {
	const cyclicConfig = `{
		"config": {
			"input_output_map": {
				"a": {"id": "a", "type": "read", "read": {"source": "b"}},
				"b": {"id": "b", "type": "read", "read": {"source": "a"}}
			}
		}
	}`

	var doc map[string]any
	if err := json.Unmarshal([]byte(cyclicConfig), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := GetSchema().Validate(doc); err != nil {
		t.Fatalf("the premise is that a read cycle is SCHEMA-VALID; validation "+
			"failed instead: %v", err)
	}

	tmp := struct {
		Config *Config `json:"config"`
	}{}
	if err := json.Unmarshal([]byte(cyclicConfig), &tmp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := tmp.Config.CheckReadCycles(); err == nil {
		t.Fatal("the load-time check must refuse a schema-valid read cycle")
	}
}
