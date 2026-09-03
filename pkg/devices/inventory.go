// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package devices

// The -list-devices inventory (review finding MAP-9).
//
// WHY IT EXISTS. configs/w17-ds4.json ships two machine-specific placeholders,
// REPLACE-WITH-DS4-ID and REPLACE-WITH-COM-PORT, and until they are filled in
// the car cannot move (the load path now refuses the profile outright -- MAP-5).
// The only way to read those two values was to open the mapper's node-graph web
// UI and hunt through it: the hobbyist step this product exists to remove, on
// the giftee's own PC, by whoever is setting the car up for her.
//
// So: one flag, one JSON document, no UI, no server, no serial port opened, and
// an exit. It prints what a person filling in the profile needs and nothing
// else -- the derived id for every attached pad, the raw SDL GUID it came from,
// and every serial port Windows is offering.
//
// The id here is derived by exactly the same function the running mapper uses
// (DeriveGamepadId), which is the point: a value read from a different code
// path would be a different kind of guess.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

// GamepadInfo is one attached gamepad, as the person filling in a profile needs
// to see it.
type GamepadInfo struct {
	// Id is the value that goes into the profile's gamepad.id.
	Id string `json:"id"`
	// Name and GUID are what the id is derived FROM, printed so the derivation
	// can be checked by hand and so a support conversation has something
	// unambiguous to quote.
	Name string `json:"name"`
	GUID string `json:"guid"`
	// Bus is decoded from the GUID; see DecodeGUIDBus for how much to trust it.
	Bus string `json:"bus"`

	Axes    int `json:"axes"`
	Buttons int `json:"buttons"`
	Hats    int `json:"hats"`
}

// SerialPortInfo is one serial port. Declared here rather than in pkg/serial so
// the whole inventory document has a single owner; the caller maps its own port
// type into it.
type SerialPortInfo struct {
	Name    string `json:"name"`
	Product string `json:"product"`
}

// Inventory is the whole -list-devices document.
type Inventory struct {
	Gamepads    []GamepadInfo    `json:"gamepads"`
	SerialPorts []SerialPortInfo `json:"serial_ports"`
}

// WriteJSON renders the inventory. Empty lists are rendered as `[]` rather than
// `null`, because "no gamepad is attached" is the answer someone is most likely
// to get and `null` reads as a failure.
func (inv Inventory) WriteJSON(w io.Writer) error {
	if inv.Gamepads == nil {
		inv.Gamepads = []GamepadInfo{}
	}
	if inv.SerialPorts == nil {
		inv.SerialPorts = []SerialPortInfo{}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(inv)
}

// DecodeGUIDBus names the transport encoded in the first field of an SDL
// joystick GUID.
//
// SDL packs the bus type into the GUID's first two bytes as a native-endian
// uint16, hex-encoded byte by byte -- so on a little-endian machine a USB pad
// starts "0300" and the same pad over Bluetooth starts "0500". That is exactly
// why the derived id CHANGES between USB and Bluetooth, which is the trap
// configs/README.md warns about at the placeholder.
//
// It is a HINT, not an authority: the mapping is read from SDL's GUID layout
// rather than from an API that reports the bus, and it has not been checked on
// Windows/HIDAPI against a real DualShock 4 -- [bench-TBD]. The raw GUID is
// printed alongside it precisely so a wrong hint cannot mislead anyone who
// looks.
func DecodeGUIDBus(guid string) string {
	if len(guid) < 4 {
		return "unknown"
	}

	// Two hex bytes, little-endian: "0300" is 0x0003.
	lo, errLo := strconv.ParseUint(guid[0:2], 16, 8)
	hi, errHi := strconv.ParseUint(guid[2:4], 16, 8)
	if errLo != nil || errHi != nil {
		return "unknown"
	}
	bus := uint16(hi)<<8 | uint16(lo)

	switch bus {
	case 0x0003:
		return "usb"
	case 0x0005:
		return "bluetooth"
	case 0x0006:
		return "virtual"
	case 0x0000:
		return "unknown"
	default:
		return fmt.Sprintf("unknown (0x%04x)", bus)
	}
}

// GamepadInventory opens SDL's joystick subsystem, describes every attached
// gamepad and shuts SDL down again. It opens no serial port and starts nothing.
//
// It walks the same 16 indices as start-up enumeration, and derives ids with
// the same function, so what it prints is what the running mapper will resolve.
func GamepadInventory() ([]GamepadInfo, error) {
	if err := sdl.Init(sdl.INIT_GAMECONTROLLER); err != nil {
		return nil, fmt.Errorf("could not start SDL to look for gamepads: %w", err)
	}
	defer sdl.Quit()

	infos := []GamepadInfo{}
	for i := 0; i < 16; i++ {
		joy := sdl.JoystickOpen(i)
		if joy == nil {
			continue
		}

		guid := sdl.JoystickGetGUIDString(joy.GUID())
		name := joy.Name()
		infos = append(infos, GamepadInfo{
			Id:      DeriveGamepadId(guid, name),
			Name:    name,
			GUID:    guid,
			Bus:     DecodeGUIDBus(guid),
			Axes:    joy.NumAxes(),
			Buttons: joy.NumButtons(),
			Hats:    joy.NumHats(),
		})
		joy.Close()
	}

	return infos, nil
}
