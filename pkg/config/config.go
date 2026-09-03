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

	// W17Profile is the W17 MARKER: a config that sets it declares itself the
	// car's race-day profile and accepts the arm-chain shape rules as FATAL
	// rather than advisory (owner decision OD-9/D2(a), review finding MAP-12).
	// W17 fork addition.
	//
	// Why a marker in the file rather than a flag. Race day builds the mapper's
	// argv from a pure whitelist carrying exactly one flag,
	// -config-file-path (w17-ground-station/main/raceDayOrchestrator.js, pinned
	// by two of its own tests), so a -w17-strict flag could never be passed on
	// the path that matters. The file is the artifact that gets hand-edited on
	// the giftee's PC, so the file is where the claim belongs.
	//
	// Why it lives INSIDE the config object rather than beside it: the headless
	// bring-up unwraps the document's top-level "config" and sends only the
	// inner object (pkg/client.configPayload, MAP-1), so a sibling key would
	// never reach the server -- and the server's SetConfig is the authoritative
	// gate, the one that also covers the web editor.
	//
	// Absent or false means the config is some other rig's, and the shape rules
	// stay silent: this fork still has to load upstream configs.
	W17Profile bool `json:"w17_profile,omitempty"`
}

// IsW17Profile reports whether this config declares itself the W17 race-day
// profile. W17 fork addition (MAP-12). Nil-safe.
func (c *Config) IsW17Profile() bool {
	return c != nil && c.W17Profile
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

	// W17 fork modification (MAP-6): resolve through the accessor, not the map
	// field. The registry is mutated by the poll goroutine now -- a hot-plug
	// removes the entry outright -- so an unguarded map read here would be a
	// data race against a device being unplugged mid-drive.
	var res *devices.InputGamepad
	if res, ok = c.Ctl.deviceCtl.Gamepad(deviceId); !ok {
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
