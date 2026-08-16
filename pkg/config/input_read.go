// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import "github.com/kaack/elrs-joystick-control/pkg/util"

type ReadT struct {
	Source string `json:"source"`
}

// InputRead *** Read ****
type InputRead struct {
	Id    string        `json:"id"`
	Value util.RawValue `json:"value"`
	IsNaN bool          `json:"-"`

	// evaluating marks this node as being inside its own Eval, so a `read`
	// cycle that comes back around is cut with a nan instead of recursing
	// until the stack is gone. W17 fork addition; see _Eval. Unexported: it is
	// meaningful only within a single evaluation and must not survive a
	// marshal round trip.
	evaluating bool

	Type string `json:"type"`
	Read ReadT  `json:"read" input:"true"`
}

func (i *InputRead) Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {
	src, out, ch, nan = i._Eval(c)
	i.Value = out
	i.IsNaN = nan

	return src, out, ch, nan
}

func (i *InputRead) _Eval(c *Config) (src IOType, out util.RawValue, ch util.ChannelNumber, nan bool) {

	// W17 fork modification -- cycle guard. Upstream followed Config.IOMap with
	// no guard of any kind, so a schema-valid pair of `read` nodes pointing at
	// each other recursed until the stack was gone and took the WHOLE process
	// down -- transmitter, failsafe machinery and all (empirically demonstrated;
	// the OS-level consequence was a dead mapper, i.e. a receiver link-loss
	// failsafe, not runaway output -- but a crash is not a designed failure
	// mode).
	//
	// Re-entrancy is exactly cycle detection here: every JSON subtree unmarshals
	// to a tree, so the ONLY edge that can close a loop in the node graph is a
	// `read` following IOMap back to a top-level entry. Going around any such
	// loop re-enters the first `read` node the evaluation came through while
	// that node is still on the stack -- the condition below. A nan result then
	// flows to whatever asked, and the existing failsafe path drives the
	// channels above it to their configured neutrals: the cycle becomes an inert
	// channel, not a dead process. CheckReadCycles rejects the config at load
	// time before it gets this far; this guard is the backstop for configs built
	// programmatically or mutated after loading.
	//
	// The flag is written without synchronization, which matches how this node
	// already treats Value and IsNaN (written on every Eval). A cross-goroutine
	// collision can only produce a spurious nan for one tick, and nan is the
	// fail-safe direction.
	if i.evaluating {
		return nil, 0, -1, true
	}
	i.evaluating = true
	defer func() { i.evaluating = false }()

	var holder *IOHolder
	var ok bool

	if holder, ok = c.IOMap[i.Read.Source]; !ok {
		return nil, 0, -1, true
	}

	if holder == nil {
		return nil, 0, -1, true
	}

	return holder.IO.Eval(c)
}

func (i *InputRead) InputType() string {
	return i.Type
}

func (i *InputRead) InputValue() *util.RawValue {
	if i.IsNaN {
		return nil
	}
	return &i.Value
}

func (i *InputRead) InputId() string {
	return i.Id
}

func (i *InputRead) Children() (out *[]*IOHolder) {
	return nil
}
