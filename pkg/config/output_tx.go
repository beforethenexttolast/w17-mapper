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
//
// This only answers for the holder itself. When the holder is a wrapper rather
// than a channel node, the node that OWNS the channel number sits further down
// the subtree and must be found with channelOwners instead -- see Eval.
func failsafeFor(ic *IOHolder) util.CRSFValue {
	if fv, ok := ic.IO.(FailsafeValuer); ok {
		return fv.FailsafeValue()
	}
	return util.CRSFValue(util.CRSFCenterValue)
}

// channelOwnerMaxDepth bounds the subtree walk. The node graph unmarshals as a
// tree, so this is a backstop for one case only: `read` resolves by id through
// Config.IOMap and can therefore be pointed at itself or at a cycle.
const channelOwnerMaxDepth = 32

// channelOwners returns every InputChannel a holder can drive, itself included.
// W17 fork addition.
//
// InputChannel is the sole originator of a channel number in the whole node set
// (it is the only caller of util.ChannelNumber) and the sole FailsafeValuer, so
// any channel a subtree writes is owned by an InputChannel somewhere inside it,
// and that node is the only one that knows the channel's configured neutral.
//
// Two traversal rules, both load-bearing:
//
//   - Stop at a channel node, do not descend past it. InputChannel discards its
//     child's channel number (input_channel.go), so a channel nested under
//     another channel can never be written through this holder and must not be
//     neutralized by it.
//   - Follow `read` through Config.IOMap. Its Children() is nil, so the generic
//     traversal cannot see the node it delegates to, yet its Eval returns that
//     node's channel number.
func channelOwners(c *Config, ih *IOHolder, depth int, out []*InputChannel) []*InputChannel {
	if ih == nil || ih.IO == nil || depth > channelOwnerMaxDepth {
		return out
	}

	if ch, ok := ih.IO.(*InputChannel); ok {
		return append(out, ch)
	}

	if rd, ok := ih.IO.(*InputRead); ok {
		if c == nil {
			return out
		}
		if target, ok := c.IOMap[rd.Read.Source]; ok {
			return channelOwners(c, target, depth+1, out)
		}
		return out
	}

	children := ih.IO.Children()
	if children == nil {
		return out
	}

	for _, child := range *children {
		out = channelOwners(c, child, depth+1, out)
	}

	return out
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
//
// W17 fork modification -- wrapper stranding. Driving "the channel the holder
// reported" was only enough while every top-level entry was a channel node,
// which reports its number on every path. The `channels` array is schema-typed
// as the full input union, and 14 of the 27 node types are ASYMMETRIC: they
// propagate their child's channel number while healthy and return -1 once their
// input stops resolving (`linear`, `map`, `case`, `if`, `trim`, `switch`, `and`,
// `or` and the six comparisons). A top-level wrapper therefore wrote a slot on
// the healthy tick and reported no channel at all on the nan tick, so the old
// `ch < 1` skip left that slot holding its last value -- the original hold-last
// defect, one level up. `switch` and `map` reach the same skip without nan at
// all, by returning a default with ch = -1.
//
// Four more types (`add`, `subtract`, `min`, `max`) are transparent instead:
// they carry the number through on BOTH paths, so the slot was written, but the
// neutral was resolved off the top-level holder -- a wrapper, not a
// FailsafeValuer -- and silently fell back to center. A switch channel with a
// correctly configured OFF rail was neutralized to 992, which is inside a
// receiver's hysteresis band, so an armed channel stayed latched ON.
//
// Both are fixed the same way: when a holder's result is unusable, every
// InputChannel under it is driven to ITS OWN configured failsafe. See
// channelOwners.
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

		if !nan && ch >= 1 && ch <= 16 {
			(*i.Values)[ch-1] = util.CRSFValue(out)
			continue
		}

		//the result is unusable: either not a number, or a node that reported
		//no channel number this tick. Neither may leave a slot holding its
		//previous value, so every channel this holder owns is driven to its own
		//configured neutral -- resolved from the node that carries the number,
		//not from the top-level holder.
		owners := channelOwners(c, ic, 0, nil)

		if len(owners) == 0 {
			//nothing below carries a failsafe intent; the holder's own is the
			//only answer available, and it is center for every non-channel type
			if ch >= 1 && ch <= 16 {
				(*i.Values)[ch-1] = failsafeFor(ic)
			}
			continue
		}

		for _, owner := range owners {
			number := util.ChannelNumber(owner.Channel.Number)
			if number < 1 || number > 16 {
				continue
			}
			(*i.Values)[number-1] = owner.FailsafeValue()
		}
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
