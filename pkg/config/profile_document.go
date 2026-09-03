// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"sort"
)

// TransmitterPorts returns every distinct serial port a decoded config
// document drives, sorted. W17 fork addition (MAP-2, owner decision OD-5(a)).
//
// It exists so the headless bring-up can start the RF link on the port the
// SAVED PROFILE already names, instead of needing that port repeated on the
// command line. Race day launches the mapper through a pure argv whitelist
// carrying exactly one flag, -config-file-path
// (w17-ground-station/main/raceDayOrchestrator.js), so the profile is the only
// place a port name can come from -- and it is the right place, because a port
// that disagrees with the profile's own tx.port resolves no channels anyway.
//
// Like UnfilledPlaceholders this reads the DOCUMENT rather than the node
// graph: the caller holds a decoded document at that point, and the answer is
// needed before the config has been adopted anywhere.
func TransmitterPorts(doc any) []string {
	seen := map[string]bool{}
	scanTransmitterPorts(doc, seen)

	ports := make([]string, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return ports
}

// TransmitterNodeCount returns how many transmitter nodes a decoded config
// document declares, whether or not each of them names a port. W17 fork
// addition (review finding N5).
//
// It exists to tell two states apart that TransmitterPorts renders
// identically, because it filters `port != ""`: a profile with NO transmitter
// at all (an upstream shape, and not an error -- there is nothing to start),
// and a profile whose transmitter is there but whose tx.port was left empty
// (an editing mistake, and the operator needs to be told which field to fill).
// Saying "declares no transmitter" for the second one sends them looking for
// the wrong thing.
func TransmitterNodeCount(doc any) int {
	count := 0
	scanTransmitterNodes(doc, &count)
	return count
}

func scanTransmitterNodes(node any, count *int) {
	switch v := node.(type) {
	case map[string]any:
		// Both halves are needed. `type: tx` and a `tx` object are what
		// schema.yaml's #/definitions/tx requires of a transmitter node, and
		// the type check is what keeps the MAP that HOLDS a node keyed "tx"
		// from counting as one itself -- input_output_map's keys are the node
		// ids, and "tx" is an obvious id to choose.
		if kind, ok := v["type"].(string); ok && kind == "tx" {
			if _, ok := v["tx"].(map[string]any); ok {
				*count++
			}
		}
		for _, child := range v {
			scanTransmitterNodes(child, count)
		}
	case []any:
		for _, child := range v {
			scanTransmitterNodes(child, count)
		}
	}
}

func scanTransmitterPorts(node any, seen map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		// A transmitter node is {"type": "tx", "tx": {"port": ..., ...}}; the
		// schema uses the "tx" key for nothing else.
		if tx, ok := v["tx"].(map[string]any); ok {
			if port, ok := tx["port"].(string); ok && port != "" {
				seen[port] = true
			}
		}
		for _, child := range v {
			scanTransmitterPorts(child, seen)
		}
	case []any:
		for _, child := range v {
			scanTransmitterPorts(child, seen)
		}
	}
}

// W17MarkerKey is the marker a saved profile carries, inside its config object,
// to declare itself the W17 car's own race-day profile. It is the same key
// Config.W17Profile decodes; this constant exists so the DOCUMENT-level check
// and the struct tag cannot drift apart.
const W17MarkerKey = "w17_profile"

// DeclaresW17Marker reports whether a decoded CONFIG OBJECT carries
// `"w17_profile": true`. W17 fork addition (owner ruling OD-9/D2 addendum,
// 2026-09-04).
//
// It reads the object client.configPayload has already unwrapped -- the same
// bytes SetConfig receives -- and not the whole document, because the marker
// lives INSIDE `config` for the reason config.go states: the headless bring-up
// sends only the inner object, so a sibling key would never reach the server.
//
// Only the JSON literal `true` counts. A string "true", a 1, or an absent key
// are all "not marked": this gates a safety check, so anything it cannot read
// as an affirmative claim is not one.
func DeclaresW17Marker(doc any) bool {
	object, ok := doc.(map[string]any)
	if !ok {
		return false
	}
	marked, ok := object[W17MarkerKey].(bool)
	return ok && marked
}

// W17MarkerRefusal is the plain-language sentence the HEADLESS bring-up shows
// when the profile it was pointed at does not declare the marker. W17 fork
// addition (owner ruling OD-9/D2 addendum, 2026-09-04, closing the hole
// independent review found in the arm-chain refusal's wording).
//
// WHY THE HEADLESS PATH ONLY. `-config-file-path` is the W17 race-day path:
// the ground station builds mapper argv from a whitelist carrying exactly that
// one flag, and the file it names is the car's own profile, hand-edited on the
// giftee's PC. LintW17ArmChain is silent on an unmarked config by design -- the
// fork still has to load other people's rigs -- so before this, deleting one
// token from that file turned every arm-chain rule off on the only copy that
// matters, and the car loaded and ran with the toggle able to re-arm itself
// after a controller dropout. Refusing the unmarked file HERE closes that
// structurally rather than by wording.
//
// The web editor's SetConfig stays permissive: that is where an upstream rig's
// config is legitimately loaded, and it is not the path the car starts from.
//
// It never tells the reader to delete anything -- same rule as
// W17ArmChainRefusal, and for the same reason.
func W17MarkerRefusal(path string) string {
	return fmt.Sprintf("this saved profile does not declare itself the W17 car's profile: "+
		"the file the drive program is started with must carry \"%s\": true inside its "+
		"\"config\" object, and %s does not -- that marker is what switches on the checks "+
		"that stop the car arming itself after the controller drops out, so a profile "+
		"without it is not started from here; if this IS the car's own profile, put the "+
		"marker back (see configs/README.md), and if it belongs to a different rig, load "+
		"it in the mapper's own web editor instead",
		W17MarkerKey, path)
}
