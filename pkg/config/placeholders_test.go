// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for the unfilled-placeholder refusal (MAP-5, owner decision OD-9/D3).

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestUnfilledPlaceholdersFindsEveryDistinctValueOnce(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(`{
		"config": {
			"input_output_map": {
				"tx": {"id": "tx", "type": "tx", "tx": {
					"port": "REPLACE-WITH-COM-PORT",
					"channels": [
						{"id": "c1", "type": "channel", "channel": {"number": 1, "input": {
							"id": "gp", "type": "gamepad",
							"gamepad": {"id": "REPLACE-WITH-DS4-ID", "name": "DualShock 4"}}}},
						{"id": "c2", "type": "channel", "channel": {"number": 2, "input": {
							"id": "gp2", "type": "gamepad",
							"gamepad": {"id": "REPLACE-WITH-DS4-ID", "name": "DualShock 4"}}}}
					]}}
			}
		}
	}`), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	found := UnfilledPlaceholders(doc)
	want := []string{"REPLACE-WITH-COM-PORT", "REPLACE-WITH-DS4-ID"}

	if len(found) != len(want) {
		t.Fatalf("got %v, want %v (each distinct value exactly once, sorted)", found, want)
	}
	for i := range want {
		if found[i] != want[i] {
			t.Errorf("got %v, want %v", found, want)
			break
		}
	}
}

// TestUnfilledPlaceholdersIgnoresAFilledProfile is the guard against
// over-correcting: once the two values are real, nothing is reported and the
// profile loads. A check that cried wolf here would make the whole product
// unusable.
func TestUnfilledPlaceholdersIgnoresAFilledProfile(t *testing.T) {
	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	filled := strings.ReplaceAll(string(raw), "REPLACE-WITH-COM-PORT", "COM17")
	filled = strings.ReplaceAll(filled, "REPLACE-WITH-DS4-ID", "a1b2c3")

	var doc map[string]any
	if err := json.Unmarshal([]byte(filled), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if found := UnfilledPlaceholders(doc); len(found) != 0 {
		t.Errorf("a filled profile must report nothing, got %v", found)
	}
}

func TestPlaceholderRefusalIsOnePlainSentence(t *testing.T) {
	msg := PlaceholderRefusal([]string{"REPLACE-WITH-COM-PORT", "REPLACE-WITH-DS4-ID"})

	if msg == "" {
		t.Fatal("a refusal with findings must say something")
	}
	if strings.Count(msg, ". ") != 0 {
		t.Errorf("the refusal must be ONE sentence, got: %s", msg)
	}
	for _, want := range []string{
		"has not been matched to this computer yet",
		`"REPLACE-WITH-COM-PORT"`,
		`"REPLACE-WITH-DS4-ID"`,
		"configs/README.md",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should contain %q, got: %s", want, msg)
		}
	}
}

func TestPlaceholderRefusalIsEmptyWithNoFindings(t *testing.T) {
	if msg := PlaceholderRefusal(nil); msg != "" {
		t.Errorf("no findings must produce no sentence, got: %s", msg)
	}
}
