// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	"sync/atomic"

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

	// Unresolved reports that the last Eval could not account for every channel
	// this transmitter drives, so Values must NOT be transmitted. W17 fork
	// addition; set only by the truncation branch of Eval.
	//
	// It is a pointer for the same reason Values is: the eval loop publishes it
	// once per config and the send loop then reads through that same pointer on
	// every tick, so the published map never has to be rebuilt -- and it must
	// not be, because rebuilding it is what the send loop's configSwapGate reads
	// as a config swap.
	//
	// atomic, unlike Values: this is the signal that says "do not transmit", and
	// it is read from the send goroutine while the eval goroutine writes it, so
	// it needs a happens-before edge to be worth anything. Values itself remains
	// unsynchronized, as upstream left it -- a tick of staleness there is bounded
	// and lands on values this function has already driven to failsafe.
	Unresolved *atomic.Bool `json:"-"`

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

// channelOwnerMaxDepth bounds the recursion in the subtree walk, and truncation
// is fail-safe -- see the walk and Eval below.
//
// It is NOT a `read`-cycle backstop, and no longer needs to be: the upstream
// defect an earlier version of this comment tracked -- InputRead._Eval
// following Config.IOMap unguarded, so a `read` cycle exhausted the stack and
// took the process down before this walk was ever called -- is now closed at
// the source. InputRead carries a re-entrancy guard that turns a cycle into a
// nan (input_read.go), and CheckReadCycles refuses cyclic configs at load time
// (read_cycles.go). On a cyclic graph that still reaches this walk, the bound
// truncates it and Eval flags the transmitter unresolved, so the port is
// suppressed -- fail-safe on that path too.
//
// What the bound actually does is keep the walk itself terminating and bounded
// in cost when it is entered on a graph that is not a tree, and cap the per-tick
// work the walk adds to the send path.
//
// 256 is chosen so that it effectively never fires: a config would have to nest
// wrapper nodes 256 deep, which is far beyond anything the editor can build. If
// it does fire, the owner set the walk returns is INCOMPLETE, so which channels
// the entry drives is unknown -- and the answer to unknown state on this path is
// already settled twice in this fork (resolveChannels for no-config, and
// configSwapGate for a config swap): transmit nothing rather than a guess. Eval
// therefore flags the transmitter unresolved and the send loop suppresses.
const channelOwnerMaxDepth = 256

// channelOwners returns every InputChannel a top-level entry can drive, and
// reports whether the walk was truncated by channelOwnerMaxDepth. W17 fork
// addition.
//
// InputChannel is the sole originator of a channel number in the whole node set
// (it is the only caller of util.ChannelNumber) and the sole FailsafeValuer, so
// any channel a subtree writes is owned by an InputChannel somewhere inside it,
// and that node is the only one that knows the channel's configured neutral.
//
// The walk also ARMS every owner it reaches, resetting that node's per-pass
// evaluation state to "not evaluated" (see InputChannel.resolvedThisPass). That
// reset is what makes the state readable afterwards: the caller walks, then
// evaluates, then reads, so an owner the evaluation never reached reads as
// unevaluated rather than carrying an answer from an earlier tick. Reading such
// a field WITHOUT the reset would be unsound -- `and`, `or`, `switch` and `case`
// all exit early, so an owner can be skipped for many ticks in a row.
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
func channelOwners(c *Config, ih *IOHolder) (owners []*InputChannel, truncated bool) {
	return walkChannelOwners(c, ih, 0, nil)
}

func walkChannelOwners(c *Config, ih *IOHolder, depth int, out []*InputChannel) ([]*InputChannel, bool) {
	if ih == nil || ih.IO == nil {
		return out, false
	}

	if depth > channelOwnerMaxDepth {
		return out, true
	}

	if ch, ok := ih.IO.(*InputChannel); ok {
		ch.armEvalPass()
		return append(out, ch), false
	}

	if rd, ok := ih.IO.(*InputRead); ok {
		if c == nil {
			return out, false
		}
		if target, ok := c.IOMap[rd.Read.Source]; ok {
			return walkChannelOwners(c, target, depth+1, out)
		}
		return out, false
	}

	children := ih.IO.Children()
	if children == nil {
		return out, false
	}

	truncated := false
	for _, child := range *children {
		var childTruncated bool
		out, childTruncated = walkChannelOwners(c, child, depth+1, out)
		truncated = truncated || childTruncated
	}

	return out, truncated
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
//
// W17 fork modification -- PARTIAL subtree failure. Neutralizing on an unusable
// result was still not enough, because a subtree can lose a channel without the
// holder's result becoming unusable at all. EvalOperation (util.go), the
// InputAnd/InputOr right-operand loops and EvalRelational all IGNORE a nan
// operand and carry on, so a holder whose operands are fed by two different
// devices reports healthy on a valid channel number while one of its channels
// has stopped resolving. Executed: add{ch1 <- number, ch2 <- axis on a detached
// gamepad} transmitted ch2 = 1984 indefinitely on a healthy link, with ch2's
// configured 172 rail never applied; the `and` variant left a detached arm
// channel at 992. Every earlier test missed this because each one detached the
// WHOLE device, so the left operand went nan first and the result was genuinely
// unusable.
//
// So the holder's own result is no longer the only question asked. The walk runs
// BEFORE the evaluation and arms every channel node the entry can drive; the
// evaluation clears the arm on the ones it actually reaches; afterwards, any
// owner that did not resolve is driven to its own failsafe even when the holder
// reported healthy. That covers both ways an owner can fail to produce a value
// -- evaluated and nan, or never evaluated because an ancestor exited early --
// and it is derived from this tick's traversal rather than read back from a
// field an earlier tick left behind. See InputChannel.resolvedThisPass.
//
// One consequence worth stating: where two top-level entries drive the same
// channel, the later entry still wins, exactly as it did before. Nothing here
// makes that shape well defined; it was already last-writer-wins.
func (i *OutputTransmitter) Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {

	if i.Values == nil {
		i.Values = centeredValues()
	}
	if i.Unresolved == nil {
		i.Unresolved = &atomic.Bool{}
	}

	//if there are no channels, out is not a number
	if i.Transmitter.Channels == nil {
		//nothing to account for, so nothing to suppress -- and clearing rather
		//than leaving the flag keeps it a statement about the current config
		//instead of something that can latch
		i.Unresolved.Store(false)
		return nil, -1, -1, true
	}

	unresolved := false

	for _, ic := range *i.Transmitter.Channels {
		if ic == nil {
			continue
		}

		//walk BEFORE evaluating: the walk arms every channel node this entry can
		//drive, and the evaluation that follows is what clears the arm
		owners, truncated := channelOwners(c, ic)

		_, out, ch, nan = ic.Eval(c)

		usable := !nan && ch >= 1 && ch <= 16

		if truncated {
			//the walk did not reach the whole subtree, so the set of channels
			//this entry drives is unknown, and a channel that is unknown cannot
			//be neutralized. Suppress the port's frames rather than transmit a
			//value that cannot be accounted for, and treat the result as
			//unusable so the owners that WERE found still degrade toward their
			//failsafes instead of holding -- otherwise the first frame after the
			//condition clears would carry a stale value.
			unresolved = true
			usable = false
		}

		if usable {
			(*i.Values)[ch-1] = util.CRSFValue(out)

			//the holder resolved, but an operand under it may not have: anything
			//the evaluation did not resolve this pass must not be left holding a
			//live value. This deliberately runs AFTER the write above, so an
			//owner that owns the reported channel and failed overrides it.
			for _, owner := range owners {
				if owner.resolvedThisPass() {
					continue
				}
				number := util.ChannelNumber(owner.Channel.Number)
				if number < 1 || number > 16 {
					continue
				}
				(*i.Values)[number-1] = owner.FailsafeValue()
			}
			continue
		}

		//the result is unusable: either not a number, or a node that reported
		//no channel number this tick. Neither may leave a slot holding its
		//previous value, so every channel this holder owns is driven to its own
		//configured neutral -- resolved from the node that carries the number,
		//not from the top-level holder.
		//
		//Every owner goes, not just the ones that failed: the holder produced
		//nothing usable, so nothing under it is driving a slot this tick. A
		//`switch` that falls through to its default reaches here with every case
		//resolving perfectly well, and its channels still must not hold.
		if len(owners) == 0 {
			//nothing below carries a failsafe intent; the holder's own is the
			//only answer available, and it is center for every non-channel type.
			//An entry that legitimately owns no channel -- a top-level `axis`,
			//say -- reports ch = -1 and is left alone entirely, which is right:
			//it drives no slot, so there is no slot to neutralize.
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

	i.Unresolved.Store(unresolved)

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
