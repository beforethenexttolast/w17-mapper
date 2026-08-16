// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Config-layer tests for the hat `direction` field (2026-08-16 audit, defect
// 13). The decode semantics themselves are pinned in pkg/devices/hat_test.go
// against the SDL bitmask; what belongs here is the load surface: the JSON
// field, its validation, and the schema the editor sees.
//
// The live read path (rawDevice.HatDirection) needs an attached SDL joystick,
// which a test cannot fabricate -- the same documented limit as the failsafe
// tests. Left for the recorded bench validation.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestHatDirectionUnmarshal(t *testing.T) {
	t.Run("valid directions load", func(t *testing.T) {
		for _, dir := range []string{"up", "down", "left", "right"} {
			var h HatT
			raw := `{"number":0,"direction":"` + dir + `"}`
			if err := json.Unmarshal([]byte(raw), &h); err != nil {
				t.Errorf("%s: %v", dir, err)
			}
			if h.Direction != dir {
				t.Errorf("direction %q round-tripped to %q", dir, h.Direction)
			}
		}
	})

	t.Run("absent direction stays legacy", func(t *testing.T) {
		var h HatT
		if err := json.Unmarshal([]byte(`{"number":0}`), &h); err != nil {
			t.Fatalf("a pre-existing config without direction must keep loading: %v", err)
		}
		if h.Direction != "" {
			t.Errorf("expected the legacy empty direction, got %q", h.Direction)
		}
	})

	t.Run("a typo fails at load, not at eval", func(t *testing.T) {
		var h HatT
		err := json.Unmarshal([]byte(`{"number":0,"direction":"downn"}`), &h)
		if err == nil {
			t.Fatal("an unknown direction must be refused at load time")
		}
		if !strings.Contains(err.Error(), "downn") {
			t.Errorf("the error should name the bad value, got: %v", err)
		}
	})
}

// TestSchemaDeclaresHatDirection pins the schema surface: the field exists,
// is enum-constrained to the four cardinal directions, and is NOT required
// (pre-existing configs omit it and must keep validating).
func TestSchemaDeclaresHatDirection(t *testing.T) {
	jsonData, err := YAMLtoJSON(strings.NewReader(Schema))
	if err != nil {
		t.Fatalf("schema is not valid yaml: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		t.Fatalf("schema is not valid json: %v", err)
	}

	defs, _ := doc["definitions"].(map[string]any)
	hat, _ := defs["hat"].(map[string]any)
	props, _ := hat["properties"].(map[string]any)
	inner, _ := props["hat"].(map[string]any)
	innerProps, _ := inner["properties"].(map[string]any)

	direction, ok := innerProps["direction"].(map[string]any)
	if !ok {
		t.Fatalf("definitions.hat.properties.hat.properties.direction is missing; "+
			"the editor cannot offer per-direction hats. Have: %v", keysOf(innerProps))
	}

	enum, _ := direction["enum"].([]any)
	want := map[string]bool{"up": true, "down": true, "left": true, "right": true}
	if len(enum) != len(want) {
		t.Errorf("direction enum = %v, want the four cardinal directions", enum)
	}
	for _, v := range enum {
		s, _ := v.(string)
		if !want[s] {
			t.Errorf("unexpected direction enum value %v", v)
		}
	}

	if _, isRequired := requiredSet(inner)["direction"]; isRequired {
		t.Error("direction must not be required: pre-existing configs omit it")
	}
}

// TestSchemaValidatesHatDirection runs the compiled schema against a real
// config document both ways.
func TestSchemaValidatesHatDirection(t *testing.T) {
	const template = `{
		"config": {
			"input_output_map": {
				"gp": {"id": "gp", "type": "gamepad", "gamepad": {"id": "abc123"}},
				"h": {"id": "h", "type": "hat",
					"hat": {"number": 0, "direction": %q,
						"input": {"id": "gp2", "type": "gamepad", "gamepad": {"id": "abc123"}}}}
			}
		}
	}`

	validate := func(dir string) error {
		var doc map[string]any
		if err := json.Unmarshal([]byte(fmt.Sprintf(template, dir)), &doc); err != nil {
			t.Fatalf("setup: %v", err)
		}
		return GetSchema().Validate(doc)
	}

	if err := validate("down"); err != nil {
		t.Errorf("a cardinal direction must validate: %v", err)
	}
	if err := validate("diagonal"); err == nil {
		t.Errorf("a non-cardinal direction must be refused by the schema")
	}
}
