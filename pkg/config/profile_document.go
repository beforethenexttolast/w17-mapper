// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import "sort"

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
