// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	_ "embed"
	"sync/atomic"

	"github.com/kaack/elrs-joystick-control/pkg/devices"
)

type Config struct {
	IOMap map[string]*IOHolder `json:"input_output_map"`
	Ctl   *Controller          `json:"-"`
}

// GetInputGamepad resolves a gamepad by device id.
//
// W17 fork modification -- failsafe gap. A detached device no longer resolves.
// The registry is never pruned on removal, so an id kept resolving after its
// gamepad was unplugged and the input nodes went on reading stale axis values
// from a detached SDL handle. Reporting !ok here routes a removal into the same
// nan path as a missing device, which the eval path drives to a defined neutral.
func (c *Config) GetInputGamepad(deviceId string) (*devices.InputGamepad, bool) {
	var ok bool

	var res *devices.InputGamepad
	if res, ok = c.Ctl.deviceCtl.Gamepads[deviceId]; !ok {
		return nil, false
	}

	if !res.Attached() {
		return nil, false
	}

	return res, true
}

func NewTransmitter(port string) *OutputTransmitter {
	return &OutputTransmitter{
		//W17 fork modification: center, not zero -- see centeredValues
		Values: centeredValues(),
		//W17 fork addition: allocated up front so the eval loop can publish the
		//pointer before the first Eval runs -- see OutputTransmitter.Unresolved
		Unresolved: &atomic.Bool{},
		Transmitter: TransmitterT{
			Port:     port,
			Channels: &[]*IOHolder{},
		},
	}
}
func (c *Config) GetTransmitters() map[string]*IOHolder {
	//group serial ports by their port name
	var grouped = map[string]*IOHolder{}

	var curTransmitter *OutputTransmitter
	var ok bool
	for _, inout := range c.IOMap {
		if curTransmitter, ok = inout.IO.(*OutputTransmitter); !ok {
			continue
		}

		if _, ok = grouped[curTransmitter.Transmitter.Port]; !ok {
			//first time we see this port, add a new entry to the map
			grouped[curTransmitter.Transmitter.Port] = &IOHolder{
				IO:     NewTransmitter(curTransmitter.Transmitter.Port),
				Ctl:    c.Ctl,
				Config: c,
			}
		}

		if existing, ok := grouped[curTransmitter.Transmitter.Port].IO.(*OutputTransmitter); ok {
			if curTransmitter.Transmitter.Channels != nil {
				*existing.Transmitter.Channels = append(
					*existing.Transmitter.Channels,
					*curTransmitter.Transmitter.Channels...)
			}
		}
	}

	return grouped
}

func (c *Config) IO(name string) (*IOType, bool) {
	var ih *IOHolder
	var ok bool
	if ih, ok = c.IOMap[name]; !ok {
		return nil, false
	}
	return &ih.IO, true
}
