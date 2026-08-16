// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

type HatT struct {
	Input        *IOHolder `json:"input"`
	Number       int32     `json:"number"`
	OutputInvert bool      `json:"output_invert"`

	// Direction selects ONE direction of the hat ("up", "down", "left",
	// "right") and turns this node into a momentary button for it: truthy raw
	// while that direction's bit is set in the SDL hat bitmask (diagonals
	// count for both components), falsy raw otherwise. W17 fork addition
	// (2026-08-16 audit, defect 13).
	//
	// Empty keeps the legacy direction-blind scalar read, which cannot tell
	// up from down -- see devices.InputGamepad.Hat. Any per-direction D-pad
	// binding (the recorded D-pad-DOWN deadman plan included) must set this.
	Direction string `json:"direction"`
}

type FakeHatT HatT

// UnmarshalJSON validates the direction at LOAD time, so a typo fails the
// config apply instead of silently evaluating to nan on every tick. W17 fork
// addition. Empty stays legal: it is the legacy scalar mode.
func (h *HatT) UnmarshalJSON(data []byte) error {
	var hat = FakeHatT{}
	if err := json.Unmarshal(data, &hat); err != nil {
		return err
	}

	*h = HatT(hat)

	if h.Direction != "" {
		if _, ok := devices.HatDirections[h.Direction]; !ok {
			names := make([]string, 0, len(devices.HatDirections))
			for name := range devices.HatDirections {
				names = append(names, name)
			}
			sort.Strings(names)
			return fmt.Errorf("hat direction %q is not one of %v", h.Direction, names)
		}
	}

	return nil
}

// InputHat *** Hat ***
type InputHat struct {
	Id    string        `json:"id"`
	Value util.RawValue `json:"value"`
	IsNaN bool          `json:"-"`

	Type string `json:"type"`
	Hat  HatT   `json:"hat" input:"true"`
}

func (i *InputHat) Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {
	src, out, ch, nan = i._Eval(c)
	i.Value = out
	i.IsNaN = nan

	return src, out, ch, nan
}

func (i *InputHat) _Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {
	input := i.Hat.Input
	if input == nil {
		return nil, 0, -1, true
	}

	if src, _, _, _ = input.Eval(c); src == nil {
		return nil, 0, -1, true
	}

	var rawDevice *devices.InputGamepad
	var ok bool
	var invert util.RawValue

	switch in := (src).(type) {
	case *InputGamepad:
		if rawDevice, ok = c.GetInputGamepad(in.Gamepad.Id); !ok {
			return nil, 0, -1, true
		}

		if i.Hat.OutputInvert {
			invert = -1
		} else {
			invert = 1
		}

		//W17 fork addition: per-direction decode. A direction that fails to
		//parse -- possible only on a config built in Go, since UnmarshalJSON
		//and the schema both reject it at load -- is nan, the fail-safe
		//answer, never a silent fall-through to the direction-blind read.
		if i.Hat.Direction != "" {
			mask, known := devices.HatDirections[i.Hat.Direction]
			if !known {
				return nil, 0, -1, true
			}
			return nil, rawDevice.HatDirection(int(i.Hat.Number), mask) * invert, -1, false
		}

		return nil, rawDevice.Hat(int(i.Hat.Number)) * invert, -1, false
	default:
		return nil, 0, -1, true

	}
}

func (i *InputHat) InputType() string {
	return i.Type
}

func (i *InputHat) InputValue() *util.RawValue {
	if i.IsNaN {
		return nil
	}
	return &i.Value
}

func (i *InputHat) InputId() string {
	return i.Id
}

func (i *InputHat) Children() (out *[]*IOHolder) {
	return GetChildren(i.Hat.Input, nil)

}
