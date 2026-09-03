// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package devices

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
)

// DeriveGamepadId is the config-level device id: the first six hex digits after
// the leading one of md5("guid: <SDL GUID>, name: <name>").
//
// W17 fork modification (MAP-6/MAP-9): the derivation is separated from the SDL
// handle so it can be stated, tested and DOCUMENTED without a device. Two
// consequences follow from it and both matter on race day:
//
//   - The id survives an unplug. It is a function of the GUID and the name
//     ONLY, so the pad that comes back after a dropout derives the same id and
//     the profile's `gamepad.id` resolves again -- provided the registry is
//     repopulated, which is what the hot-plug handling in hotplug.go does.
//   - The id does NOT survive a change of transport. SDL's GUID encodes the bus
//     alongside the vendor/product, so the same DualShock 4 has one id over USB
//     and a different one over Bluetooth, and configs/README.md says so at the
//     placeholder.
//
// The `[1:7]` slice (not `[0:6]`) is upstream's and is kept byte-for-byte: it is
// what every id already in a saved config was derived with.
func DeriveGamepadId(guid, name string) string {
	// on linux, the SDL GUID is not unique, use the name as well
	combined := fmt.Sprintf("guid: %s, name: %s", guid, name)
	hash := md5.Sum([]byte(combined))
	return hex.EncodeToString(hash[:])[1:7]
}

func GetJoyStickId(joystick *sdl.Joystick) string {
	return DeriveGamepadId(sdl.JoystickGetGUIDString(joystick.GUID()), joystick.Name())
}

// sdlOpener is the production joystickOpener: SDL_JoystickOpen by device index.
type sdlOpener struct{}

func (sdlOpener) Open(deviceIndex int) (*InputGamepad, bool) {
	stick := sdl.JoystickOpen(deviceIndex)
	if stick == nil {
		return nil, false
	}
	dev := NewDevice(stick)
	return &dev, true
}

// enumerateDevices opens every joystick index SDL currently offers. Used once,
// at start-up; everything after that arrives as a hot-plug event.
//
// W17 fork modification (MAP-6): it takes the opener so the same collision rule
// is exercised in tests, and returns the map rather than reaching for SDL
// directly.
func enumerateDevices(opener joystickOpener) map[string]*InputGamepad {
	devices := make(map[string]*InputGamepad)

	for i := 0; i < 16; i++ {
		dev, ok := opener.Open(i)
		if !ok {
			continue
		}

		if _, taken := devices[dev.Id]; taken {
			//device is already on the map, re-assign the ID to include index
			dev.Id = fmt.Sprintf("%s_%d", dev.Id, i)
		}
		devices[dev.Id] = dev
	}

	return devices
}

// EnumerateDevices is the start-up enumeration against real SDL.
func EnumerateDevices() map[string]*InputGamepad {
	return enumerateDevices(sdlOpener{})
}
