// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Guards the embedded schema.yaml around the W17 `failsafe` channel field.
//
// GetSchema/_NewSchema PANIC on a malformed schema, and they are only reached at
// runtime — a broken edit to schema.yaml survives `go build` and the rest of the
// suite, then takes the app down at startup. These tests exercise that path.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// TestSchemaCompiles is the panic guard: it forces the YAML->JSON conversion and
// the Draft2020 compile that the app performs on first use.
func TestSchemaCompiles(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("schema.yaml failed to compile: %v", r)
		}
	}()

	if GetSchema() == nil {
		t.Fatal("GetSchema returned nil")
	}
}

// TestSchemaDeclaresChannelFailsafe pins the field's presence and its default, so
// the UI keeps offering center rather than silently dropping to 0.
func TestSchemaDeclaresChannelFailsafe(t *testing.T) {
	jsonData, err := YAMLtoJSON(strings.NewReader(Schema))
	if err != nil {
		t.Fatalf("schema is not valid yaml: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		t.Fatalf("schema is not valid json: %v", err)
	}

	defs, _ := doc["definitions"].(map[string]any)
	channel, _ := defs["channel"].(map[string]any)
	props, _ := channel["properties"].(map[string]any)
	inner, _ := props["channel"].(map[string]any)
	innerProps, _ := inner["properties"].(map[string]any)

	failsafe, ok := innerProps["failsafe"].(map[string]any)
	if !ok {
		t.Fatalf("definitions.channel.properties.channel.properties.failsafe is missing; "+
			"the config UI cannot offer a per-channel neutral. Have: %v", keysOf(innerProps))
	}

	if got := failsafe["type"]; got != "integer" {
		t.Errorf("failsafe type = %v, want integer", got)
	}

	// JSON numbers decode as float64.
	if got, _ := failsafe["default"].(float64); int(got) != util.CRSFCenterValue {
		t.Errorf("failsafe default = %v, want %d (center). A 0 default would "+
			"decode as full negative deflection, not neutral", failsafe["default"], util.CRSFCenterValue)
	}

	if _, isRequired := requiredSet(inner)["failsafe"]; isRequired {
		t.Error("failsafe must not be required: pre-existing configs omit it and " +
			"must keep loading with the center default")
	}
}

// TestChannelUnmarshalDefaultsFailsafe covers the back-compat path an existing
// saved config takes: no `failsafe` key at all must yield center, never zero.
func TestChannelUnmarshalDefaultsFailsafe(t *testing.T) {
	for name, raw := range map[string]string{
		"omitted":              `{"number":1,"input":null}`,
		"explicit switch rail": `{"number":5,"input":null,"failsafe":172}`,
	} {
		var ch ChannelT
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatalf("%s: unmarshal failed: %v", name, err)
		}
		if ch.Failsafe == nil {
			t.Fatalf("%s: Failsafe left nil", name)
		}

		want := util.RawValue(util.CRSFCenterValue)
		if name != "omitted" {
			want = 172
		}
		if *ch.Failsafe != want {
			t.Errorf("%s: Failsafe = %d, want %d", name, *ch.Failsafe, want)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func requiredSet(node map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	req, _ := node["required"].([]any)
	for _, r := range req {
		if s, ok := r.(string); ok {
			out[s] = struct{}{}
		}
	}
	return out
}
