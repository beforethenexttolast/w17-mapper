// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for the transmitter-port scan the headless self-start reads (MAP-2,
// owner decision OD-5(a)).

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestTransmitterPortsReadsTheShippedProfile is the one that matters: the
// self-start must find exactly the port the committed profile declares, so
// race day -- which can pass no port at all -- starts the link on the right
// one.
func TestTransmitterPortsReadsTheShippedProfile(t *testing.T) {
	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	filled := strings.ReplaceAll(string(raw), "REPLACE-WITH-COM-PORT", "COM17")

	var doc map[string]any
	if err := json.Unmarshal([]byte(filled), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ports := TransmitterPorts(doc)
	if len(ports) != 1 || ports[0] != "COM17" {
		t.Errorf("the shipped profile must declare exactly one transmitter port, got %v", ports)
	}
}

func TestTransmitterPortsWithoutATransmitter(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(
		`{"config":{"input_output_map":{"n":{"id":"n","type":"number","number":{"output":0}}}}}`,
	), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if ports := TransmitterPorts(doc); len(ports) != 0 {
		t.Errorf("a config with no tx node has no port, got %v", ports)
	}
}

// TestTransmitterPortsDeduplicatesAndSorts covers the multi-transmitter shape:
// several tx nodes on the SAME port are one link, several on different ports
// are the case the self-start refuses to guess between.
func TestTransmitterPortsDeduplicatesAndSorts(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(`{"config":{"input_output_map":{
		"a": {"id":"a","type":"tx","tx":{"port":"COM9","channels":[]}},
		"b": {"id":"b","type":"tx","tx":{"port":"COM3","channels":[]}},
		"c": {"id":"c","type":"tx","tx":{"port":"COM9","channels":[]}}
	}}}`), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ports := TransmitterPorts(doc)
	if len(ports) != 2 || ports[0] != "COM3" || ports[1] != "COM9" {
		t.Errorf("got %v, want [COM3 COM9] -- distinct and sorted", ports)
	}
}
