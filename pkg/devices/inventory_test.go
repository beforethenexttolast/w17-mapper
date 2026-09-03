// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package devices

// Tests for the -list-devices inventory (review finding MAP-9).
//
// GamepadInventory itself needs an attached pad and is therefore not exercised
// here -- no unattended session can plug one in. What IS pinned is everything
// the person filling in a profile depends on being right: the derivation the
// printed id uses, the bus decode, and the document's shape.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestTheInventoryRendersTheFieldsAProfileNeeds. Someone is reading this to
// copy two values into configs/w17-ds4.json; every field they need has to be
// present and named the same way the profile names it.
func TestTheInventoryRendersTheFieldsAProfileNeeds(t *testing.T) {
	inv := Inventory{
		Gamepads: []GamepadInfo{{
			Id: padID(), Name: padName, GUID: padGUID, Bus: "usb",
			Axes: 6, Buttons: 16, Hats: 1,
		}},
		SerialPorts: []SerialPortInfo{{Name: "COM7", Product: "USB Serial Device"}},
	}

	var out bytes.Buffer
	if err := inv.WriteJSON(&out); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var back Inventory
	if err := json.Unmarshal(out.Bytes(), &back); err != nil {
		t.Fatalf("the inventory must be valid JSON: %v\n%s", err, out.String())
	}
	if len(back.Gamepads) != 1 || back.Gamepads[0].Id != padID() {
		t.Fatalf("the gamepad row did not survive the round trip: %#v", back.Gamepads)
	}
	if back.Gamepads[0].GUID != padGUID {
		t.Errorf("the raw GUID must be printed -- it is the only field a wrong bus " +
			"decode cannot mislead someone about")
	}
	if len(back.SerialPorts) != 1 || back.SerialPorts[0].Name != "COM7" {
		t.Errorf("the serial port row did not survive: %#v", back.SerialPorts)
	}

	// The field names are what a written instruction can refer to, so pin them.
	for _, key := range []string{`"id"`, `"name"`, `"guid"`, `"bus"`, `"serial_ports"`} {
		if !strings.Contains(out.String(), key) {
			t.Errorf("the document has no %s field:\n%s", key, out.String())
		}
	}
}

// TestAnEmptyInventoryIsEmptyListsNotNull: "no gamepad is attached" is the most
// likely answer someone gets, and `null` reads as a failure.
func TestAnEmptyInventoryIsEmptyListsNotNull(t *testing.T) {
	var out bytes.Buffer
	if err := (Inventory{}).WriteJSON(&out); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	if strings.Contains(out.String(), "null") {
		t.Errorf("an empty inventory renders null:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"gamepads": []`) {
		t.Errorf("an empty inventory must still show the gamepads key:\n%s", out.String())
	}
}

// TestTheBusDecodeExplainsWhyTheIdChanges. The bus lives in the GUID, which is
// why the same pad derives a different id over USB and over Bluetooth -- the
// trap configs/README.md warns about at the placeholder.
func TestTheBusDecodeExplainsWhyTheIdChanges(t *testing.T) {
	cases := map[string]string{
		padGUID:                            "usb",
		"050000004c050000cc09000000810000": "bluetooth",
		"060000004c050000cc09000000810000": "virtual",
		"000000000000000000000000000000":   "unknown",
		"":                                 "unknown",
		"zz00":                             "unknown",
	}
	for guid, want := range cases {
		if got := DecodeGUIDBus(guid); got != want {
			t.Errorf("DecodeGUIDBus(%q) = %q, want %q", guid, got, want)
		}
	}

	if got := DecodeGUIDBus("ff0000004c050000cc09000000810000"); !strings.HasPrefix(got, "unknown") {
		t.Errorf("an unrecognised bus must say so plainly, got %q", got)
	}
}

// TestThePrintedIdIsTheOneTheMapperResolves is the whole point of the flag: a
// value read from a different code path would be a different kind of guess.
func TestThePrintedIdIsTheOneTheMapperResolves(t *testing.T) {
	pad, _ := newFakePad(1)

	if got := DeriveGamepadId(padGUID, padName); got != pad.Id {
		t.Errorf("the inventory would print %q while the registry resolves %q", got, pad.Id)
	}
}
