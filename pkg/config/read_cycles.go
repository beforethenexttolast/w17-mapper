// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"sort"
	"strings"
)

// CheckReadCycles reports an error when the config's `read` nodes form a cycle
// through Config.IOMap. W17 fork addition; called by the server's SetConfig
// before a config is applied.
//
// Why load time, when InputRead._Eval already guards itself: the runtime guard
// turns a cycle into an inert (nan, failsafed) channel, which is safe but
// silent -- the user sees a channel pinned at its neutral with no explanation.
// A config whose reads form a loop can never produce a value from that loop
// (`read` re-evaluates its target; it does not read a cached value), so it is
// an authoring error on every path that can reach it, and the honest answer is
// to refuse it with the loop spelled out while the author is still looking at
// the editor.
//
// The check is deliberately static and conservative: it follows every `read`
// edge in every entry's subtree, including edges an evaluation might skip at
// runtime (`and`, `or`, `switch` and `case` exit early). A cycle that is
// dormant today is one operand flip away from being evaluated, so it is
// rejected all the same.
//
// A `read` whose source is missing from the map is NOT an error here -- it
// evaluates to nan, the same answer it has always had.
func (c *Config) CheckReadCycles() error {
	if c == nil || c.IOMap == nil {
		return nil
	}

	// Three-colour DFS over top-level entries. Edges: entry -> every read
	// source in its subtree. Only `read` can point back into IOMap, so cycles
	// among entries are exactly the cycles the node graph can contain.
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}

	var visit func(name string, stack []string) error
	visit = func(name string, stack []string) error {
		switch state[name] {
		case visiting:
			// Close the loop in the report: everything from the first
			// occurrence of name onward, plus name again.
			loop := append(trimToFirst(stack, name), name)
			return fmt.Errorf("read cycle: %s -- a read loop can never produce "+
				"a value and its channels would sit at failsafe; break the loop "+
				"or remove one of the reads", strings.Join(loop, " -> "))
		case done:
			return nil
		}

		holder, ok := c.IOMap[name]
		if !ok || holder == nil {
			// Dangling read target: evaluates to nan, not a cycle.
			return nil
		}

		state[name] = visiting
		for _, target := range readTargets(holder) {
			if err := visit(target, append(stack, name)); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}

	// Sorted for a deterministic first-reported cycle.
	names := make([]string, 0, len(c.IOMap))
	for name := range c.IOMap {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// readTargets returns the `read` sources reachable in this holder's subtree
// without crossing IOMap. The traversal is over in-memory trees (each JSON
// subtree unmarshals to a tree), so it needs no depth bound of its own.
func readTargets(ih *IOHolder) []string {
	var out []string
	var walk func(ih *IOHolder)
	walk = func(ih *IOHolder) {
		if ih == nil || ih.IO == nil {
			return
		}
		if rd, ok := ih.IO.(*InputRead); ok {
			out = append(out, rd.Read.Source)
			return // a read has no children of its own
		}
		if children := ih.IO.Children(); children != nil {
			for _, child := range *children {
				walk(child)
			}
		}
	}
	walk(ih)
	return out
}

// trimToFirst returns stack from the first occurrence of name onward, so the
// reported loop starts at the entry that closes it rather than at whatever
// entry the DFS happened to start from.
func trimToFirst(stack []string, name string) []string {
	for i, s := range stack {
		if s == name {
			return stack[i:]
		}
	}
	return stack
}
