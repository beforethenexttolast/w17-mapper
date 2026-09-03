// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"sort"
	"strings"
)

// PlaceholderPrefix marks a value in a shipped profile that is MACHINE-SPECIFIC
// and therefore cannot be committed: the ELRS transmitter's serial port and the
// gamepad's SDL-derived id are different on every computer, so
// configs/w17-ds4.json ships them as REPLACE-WITH-COM-PORT and
// REPLACE-WITH-DS4-ID. W17 fork addition.
const PlaceholderPrefix = "REPLACE-WITH-"

// UnfilledPlaceholders returns every distinct placeholder value still present
// anywhere in a decoded config document, sorted. W17 fork addition (MAP-5,
// owner decision OD-9/D3).
//
// The state it catches is FAIL-SAFE but SILENT, which is the whole problem. An
// unresolved gamepad id never matches a device, so every input reads nan and
// every channel sits on its failsafe -- arm on the 172 disarm rail -- and an
// unfilled tx.port simply never matches the port the send loop owns, so no
// channel frame resolves. The car is safe and completely dead, and until this
// check existed NOTHING said so: the placeholders are legal strings, so they
// passed the schema, the read-cycle check, LintConfig, the profile tests and
// the ground station's pre-launch checks alike. The operator's only symptom
// was a car that would not arm.
//
// The scan is over the DOCUMENT rather than the node graph on purpose. Both
// callers -- pkg/client's headless bring-up and the server's SetConfig -- hold
// a decoded document at the point they need the answer, so one implementation
// serves both and they cannot drift into disagreeing about what counts. It
// also means a placeholder in a field nobody has thought of yet is still
// caught, which a typed walk over tx.port and gamepad.id would miss.
func UnfilledPlaceholders(doc any) []string {
	seen := map[string]bool{}
	scanPlaceholders(doc, seen)

	found := make([]string, 0, len(seen))
	for value := range seen {
		found = append(found, value)
	}
	sort.Strings(found)
	return found
}

func scanPlaceholders(node any, seen map[string]bool) {
	switch v := node.(type) {
	case string:
		if strings.HasPrefix(v, PlaceholderPrefix) {
			seen[v] = true
		}
	case map[string]any:
		for _, child := range v {
			scanPlaceholders(child, seen)
		}
	case []any:
		for _, child := range v {
			scanPlaceholders(child, seen)
		}
	}
}

// PlaceholderRefusal is the ONE plain-language sentence a refusal shows, in
// the headless bring-up and in the web editor alike. W17 fork addition.
//
// It is deliberately not phrased as an error about a config file: the person
// reading it is at a bench with a car that will not move, and what they need
// to know is that this profile has not been matched to THIS computer yet, and
// where the instructions are.
func PlaceholderRefusal(found []string) string {
	if len(found) == 0 {
		return ""
	}

	quoted := make([]string, 0, len(found))
	for _, value := range found {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	values := strings.Join(quoted, ", ")

	if len(found) == 1 {
		return fmt.Sprintf("this saved profile has not been matched to this computer "+
			"yet: it still contains the placeholder value %s, which has to be replaced "+
			"with the real one for this machine before the car can be driven -- see "+
			"configs/README.md", values)
	}

	return fmt.Sprintf("this saved profile has not been matched to this computer "+
		"yet: it still contains the placeholder values %s, which have to be replaced "+
		"with the real ones for this machine before the car can be driven -- see "+
		"configs/README.md", values)
}
