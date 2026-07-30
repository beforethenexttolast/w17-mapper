// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

type TransmitterT struct {
	Name     string       `json:"name"`
	Port     string       `json:"port"`
	Channels *[]*IOHolder `json:"channels"`
}

// FailsafeValuer is implemented by channel nodes that can supply a defined
// neutral for their own channel when their input cannot be evaluated.
// W17 fork addition.
type FailsafeValuer interface {
	FailsafeValue() util.CRSFValue
}

// centeredValues returns a channel array pre-loaded with the CRSF center, for
// use as the initial state of a transmitter. W17 fork addition: a zero-valued
// array is NOT neutral -- 0 is below the nominal CRSF range and a receiver
// normalizing against the 172/992/1811 anchors reads it as full negative
// deflection, so unmapped channels would command hard-over outputs.
func centeredValues() *[16]util.CRSFValue {
	var values [16]util.CRSFValue
	for i := range values {
		values[i] = util.CRSFValue(util.CRSFCenterValue)
	}
	return &values
}

// OutputTransmitter *** Output Transmitter Device ***
type OutputTransmitter struct {
	Id     string              `json:"id"`
	Values *[16]util.CRSFValue `json:"values"`

	Type        string       `json:"type"`
	Transmitter TransmitterT `json:"tx" input:"true"`
	Holder      *IOHolder    `json:"-"`
}

// failsafeFor resolves the neutral a channel node wants, falling back to center
// for node types that do not carry a failsafe intent. W17 fork addition.
func failsafeFor(ic *IOHolder) util.CRSFValue {
	if fv, ok := ic.IO.(FailsafeValuer); ok {
		return fv.FailsafeValue()
	}
	return util.CRSFValue(util.CRSFCenterValue)
}

// Eval assembles the 16-channel CRSF array from the transmitter's channel nodes.
//
// W17 fork modification -- failsafe gap. Upstream skipped a channel whose input
// evaluated to nan, which left the previous tick's value in the persistent
// Values array. Because the array is mutated in place and never reset, an input
// that stopped resolving (unplugged gamepad, removed device) froze its channel
// at its last value indefinitely, while the mapper kept transmitting well-formed
// CRSF at full rate. The receiver saw a healthy link with a stale payload, so no
// link-loss failsafe could fire -- throttle stuck wherever it was.
//
// A nan channel is now driven to its configured failsafe value instead of being
// skipped. See ChannelT.Failsafe.
func (i *OutputTransmitter) Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {

	if i.Values == nil {
		i.Values = centeredValues()
	}

	//if there are no channels, out is not a number
	if i.Transmitter.Channels == nil {
		return nil, -1, -1, true
	}

	for _, ic := range *i.Transmitter.Channels {
		if ic == nil {
			continue
		}
		_, out, ch, nan = ic.Eval(c)

		//a nan still carries its channel number, so the channel can be
		//neutralized rather than left holding a stale value
		if ch < 1 || ch > 16 {
			continue
		}

		if nan {
			(*i.Values)[ch-1] = failsafeFor(ic)
			continue
		}

		(*i.Values)[ch-1] = util.CRSFValue(out)
	}

	return nil, -1, -1, true
}

func (i *OutputTransmitter) InputType() string {
	return i.Type
}

func (i *OutputTransmitter) InputValue() *util.RawValue {
	//this is really a no-op, since transmitter state cannot be represented by a single value
	return nil
}

func (i *OutputTransmitter) InputId() string {
	return i.Id
}

func (i *OutputTransmitter) Children() (out *[]*IOHolder) {
	return i.Transmitter.Channels
}
