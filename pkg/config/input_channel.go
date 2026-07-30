// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	"encoding/json"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

type ChannelT struct {
	Number  int32          `json:"number"`
	Input   *IOHolder      `json:"input"`
	CRSFMax *util.RawValue `json:"crsf_max"`
	CRSFMin *util.RawValue `json:"crsf_min"`
	RawMax  *util.RawValue `json:"raw_max"`
	RawMin  *util.RawValue `json:"raw_min"`

	// Failsafe is the CRSF value this channel is driven to when its input
	// subtree cannot be evaluated (device unresolvable or detached, missing
	// input node). W17 fork addition; defaults to util.CRSFCenterValue.
	//
	// Center is right for proportional channels (throttle, steering, gimbal).
	// Switch-like channels should set this to their OFF rail instead: a
	// receiver that applies hysteresis around center will HOLD the previous
	// switch state at center, so an arm or DRS channel left at center stays
	// latched in whatever state it was in when the input died.
	Failsafe *util.RawValue `json:"failsafe"`
}

// InputChannel *** Channel ***
type InputChannel struct {
	Id    string        `json:"id"`
	Value util.RawValue `json:"value"`
	IsNaN bool          `json:"-"`

	Type    string    `json:"type"`
	Channel ChannelT  `json:"channel" input:"true"`
	Holder  *IOHolder `json:"-"`
}

type FakeChannelT ChannelT

func (c *ChannelT) UnmarshalJSON(data []byte) error {

	var axis = FakeChannelT{}
	if err := json.Unmarshal(data, &axis); err != nil {
		return err
	}

	*c = ChannelT(axis)

	//set defaults
	if c.CRSFMax == nil {
		(*c).CRSFMax = new(util.RawValue)
		*(*c).CRSFMax = util.CRSFMaxValue
	}

	if c.CRSFMin == nil {
		(*c).CRSFMin = new(util.RawValue)
		*(*c).CRSFMin = util.CRSFMinValue
	}

	if c.RawMax == nil {
		(*c).RawMax = new(util.RawValue)
		*(*c).RawMax = util.MaxRaw
	}

	if c.RawMin == nil {
		(*c).RawMin = new(util.RawValue)
		*(*c).RawMin = util.MinRaw
	}

	//W17 fork addition: absent failsafe means center, never 0
	if c.Failsafe == nil {
		(*c).Failsafe = new(util.RawValue)
		*(*c).Failsafe = util.CRSFCenterValue
	}

	return nil
}

// FailsafeValue reports the CRSF value this channel must be driven to when its
// input cannot be evaluated. W17 fork addition; satisfies FailsafeValuer.
//
// The nil guard matters: a config built in Go rather than unmarshalled from JSON
// never runs UnmarshalJSON, so the default is applied here too.
func (i *InputChannel) FailsafeValue() util.CRSFValue {
	if i.Channel.Failsafe == nil {
		return util.CRSFValue(util.CRSFCenterValue)
	}
	return util.CRSFValue(*i.Channel.Failsafe)
}

func (i *InputChannel) Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {
	src, out, ch, nan = i._Eval(c)
	i.Value = out
	i.IsNaN = nan

	return src, out, ch, nan
}
func (i *InputChannel) _Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {

	//no input, result is not a number
	if i.Channel.Input == nil {
		return nil, 0, util.ChannelNumber(i.Channel.Number), true
	}

	_, out, _, nan = i.Channel.Input.Eval(c)
	if nan {
		return nil, 0, util.ChannelNumber(i.Channel.Number), true
	}

	return nil, util.MapRange(out, *i.Channel.RawMin, *i.Channel.RawMax, *i.Channel.CRSFMin, *i.Channel.CRSFMax), util.ChannelNumber(i.Channel.Number), false
}

func (i *InputChannel) InputType() string {
	return i.Type
}

func (i *InputChannel) InputValue() *util.RawValue {
	if i.IsNaN {
		return nil
	}
	return &i.Value
}

func (i *InputChannel) InputId() string {
	return i.Id
}

func (i *InputChannel) Children() (out *[]*IOHolder) {
	return GetChildren(i.Channel.Input, nil)
}
