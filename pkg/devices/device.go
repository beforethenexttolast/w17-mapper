// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package devices

import (
	"encoding/json"
	"github.com/kaack/elrs-joystick-control/pkg/util"
	"github.com/veandco/go-sdl2/sdl"
)

type InputGamepad struct {
	Id   string `json:"id"`
	Name string `json:"name"`

	Joy *sdl.Joystick `json:"-"`
}

// Attached reports whether this gamepad's SDL handle still refers to a present
// device. W17 fork addition.
//
// The registry is built once in Controller.Init and never pruned: the polling
// loop discards the SDL event body, so a device removal is never reflected in
// Controller.Gamepads. Without this check a physically unplugged gamepad still
// resolves by id, and Axis/Button/Hat keep reading the stale, frozen values held
// by an open-but-detached SDL_Joystick -- which the config layer cannot
// distinguish from live input. Gating resolution on this turns a removal into a
// clean "unresolvable device", which the eval path already neutralizes.
//
// A nil handle counts as detached: an InputGamepad with no joystick cannot be
// read from.
func (d *InputGamepad) Attached() bool {
	return d.Joy != nil && d.Joy.Attached()
}

func (d *InputGamepad) Axis(axis int) util.RawValue {
	return util.RawValue(d.Joy.Axis(axis))
}

func (d *InputGamepad) Button(button int) util.RawValue {
	return util.RawValue(d.Joy.Button(button))
}

// Hat is the legacy DIRECTION-BLIND read, kept only so pre-existing configs
// with a plain `hat` node keep the behaviour they had.
//
// It is close to meaningless: SDL reports a hat as a BITMASK (centered 0,
// up 1, right 2, down 4, left 8, diagonals OR-ed), and this maps that mask
// through a [-1, 1] input range -- so every pressed direction clamps to
// MaxRaw and centered reads as mid-scale. Any binding that cares WHICH way
// the hat points must use HatDirection instead (config: `hat` node with a
// `direction` field). W17 fork note; 2026-08-16 audit, defect 13.
func (d *InputGamepad) Hat(hat int) util.RawValue {
	return util.MapRange(util.RawValue(d.Joy.Hat(hat)), -1, 1, util.MinRaw, util.MaxRaw)
}

// HatDirections maps the config-level direction names onto the SDL hat
// bitmask. W17 fork addition (2026-08-16 audit, defect 13: hats were
// direction-blind, which broke any per-direction D-pad binding).
var HatDirections = map[string]uint8{
	"up":    sdl.HAT_UP,
	"right": sdl.HAT_RIGHT,
	"down":  sdl.HAT_DOWN,
	"left":  sdl.HAT_LEFT,
}

// DecodeHatDirection is the pure per-direction decode: truthy raw when the
// direction's bit is set in the SDL hat state, falsy raw otherwise. Diagonals
// therefore activate BOTH of their component directions -- holding a D-pad
// diagonal still counts as, say, DOWN, which is what a deadman-style binding
// needs. W17 fork addition; separated from the SDL handle so it can be tested
// without hardware.
func DecodeHatDirection(state uint8, direction uint8) util.RawValue {
	if state&direction != 0 {
		return util.DefaultTruthyRawValue
	}
	return util.DefaultFalsyRawValue
}

// HatDirection reads one direction of one hat as a momentary button. See
// DecodeHatDirection for the semantics. W17 fork addition.
func (d *InputGamepad) HatDirection(hat int, direction uint8) util.RawValue {
	return DecodeHatDirection(d.Joy.Hat(hat), direction)
}

func (d *InputGamepad) Close() {
	d.Joy.Close()
}

func (d *InputGamepad) InstanceId() int32 {
	return int32(d.Joy.InstanceID())
}
func (d *InputGamepad) Axes() int32 {
	return int32(d.Joy.NumAxes())
}

func (d *InputGamepad) Buttons() int32 {
	return int32(d.Joy.NumButtons())
}

func (d *InputGamepad) Hats() int32 {
	return int32(d.Joy.NumHats())
}

func NewDevice(joy *sdl.Joystick) InputGamepad {
	return InputGamepad{
		Id:   GetJoyStickId(joy),
		Name: joy.Name(),
		Joy:  joy,
	}
}

type FakeInputGamepad InputGamepad

func (d *InputGamepad) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		FakeInputGamepad
		Axes    int32 `json:"axes"`
		Buttons int32 `json:"buttons"`
		Hats    int32 `json:"hats"`
	}{
		FakeInputGamepad: FakeInputGamepad(*d),
		Axes:             d.Axes(),
		Buttons:          d.Buttons(),
		Hats:             d.Hats(),
	})
}
