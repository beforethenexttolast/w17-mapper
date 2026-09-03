// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	"errors"
	"fmt"
	"github.com/kaack/elrs-joystick-control/pkg/util"
	"golang.org/x/exp/maps"
	"gopkg.in/tomb.v2"
	"sync/atomic"
	"time"
)

func (c *Controller) alertEvalChan() {
	c.EvalEventCount += 1 // it's okay if it overflows
	select {
	case c.EvalEventChan <- c.EvalEventCount:
	//no-op
	default:
		//no-op
	}
}

func (c *Controller) AlertStreamChan() {
	c.StreamEventCount += 1 // it's okay if it overflows
	select {
	case c.StreamEventChan <- c.EvalEventCount:
	//no-op
	default:
		//no-op
	}
}

func (c *Controller) initEvalChan() {
	c.EvalEventCount = int32(0)
	c.EvalEventChan = make(chan int32)
}

func (c *Controller) initStreamChan() {
	c.StreamEventCount = int32(0)
	c.StreamEventChan = make(chan int32)
}

func EvalAll(config *Config) {
	if config == nil {
		return
	}

	var io *IOHolder
	//evaluate all config items
	for _, io = range config.IOMap {
		if io == nil {
			continue
		}
		io.Eval(config)
	}
}

// applyConfig prepares a newly applied config for transmission: it evaluates
// the config, then the synthetic per-port transmitters, and returns those
// holders together with the port -> channel-array map the send loop reads.
// W17 fork addition.
//
// Evaluating BEFORE publishing is the point. GetTransmitters builds a fresh
// OutputTransmitter per port through NewTransmitter, whose array starts at
// centeredValues() -- all 16 slots at 992 -- and copies only the channel-node
// lists across. Only the DeviceEventChan branch of EvalLoop ever evaluated
// those synthetic holders, so publishing them first exposed an array in which
// EVERY channel read 992, not just the ones the new config no longer maps,
// until some device event happened to arrive.
//
// This does NOT make a config swap safe on its own, and cannot: a channel the
// new config no longer maps is written by no node, so it keeps the 992 the
// fresh array was seeded with, permanently. That is what the send loop's
// configSwapGate covers.
//
// The two published maps are built here together and swapped together, and both
// hold POINTERS the transmitters keep updating in place. Neither map is rebuilt
// between config applications, which matters: the send loop's configSwapGate
// reads a new map pointer as a config swap and opens a suppression window.
func applyConfig(config *Config) ([]*IOHolder, map[string]*[16]util.CRSFValue, map[string]*atomic.Bool) {
	holders := maps.Values(config.GetTransmitters())

	EvalAll(config)

	published := map[string]*[16]util.CRSFValue{}
	unresolved := map[string]*atomic.Bool{}
	for _, holder := range holders {
		tx, ok := holder.IO.(*OutputTransmitter)
		if !ok {
			continue
		}
		tx.Eval(holder.Config)
		published[tx.Transmitter.Port] = tx.Values
		unresolved[tx.Transmitter.Port] = tx.Unresolved
	}

	return holders, published, unresolved
}

// evalHeartbeatInterval is how often the eval loop re-evaluates the synthetic
// transmitters on its own, with no event of any kind. W17 fork addition.
//
// The gap it closes (2026-08-16 audit, defect 2 / RESIDUAL C): every path that
// re-evaluated a transmitter was EVENT-DRIVEN, and every event source was
// droppable or subscriber-dependent. A device removal produces one burst of SDL
// events, AlertDeviceChan forwards them as a NON-BLOCKING send on an unbuffered
// channel with competing receivers (this loop and any GetGamepadStream RPC),
// and the 25 ms tickers that re-fire evaluation all live inside streaming RPC
// handlers. With zero gRPC subscribers, one dropped removal alert therefore
// left the stale pre-removal values transmitting at the full CRSF rate forever
// -- the hold-last defect all over again, one layer up, and the failsafe work
// in the Eval path never ran because nothing called Eval.
//
// The heartbeat makes neutralization subscriber-independent: whatever happens
// to the alerts, every transmitter is re-evaluated at least this often, and
// Eval is where detached devices stop resolving and failsafes are applied. The
// bound that matters downstream: an input death is reflected in the published
// arrays within one heartbeat interval, plus one send-loop tick before it is
// on the wire -- comfortably inside the 500 ms link-timeout the W17 control
// firmware needs to see, and the same 25 ms cadence the streaming RPCs already
// impose whenever a UI is connected, so a headless mapper now evaluates no
// differently from a watched one.
//
// The device-event branch stays: it reacts faster when an alert does arrive.
// The heartbeat is the floor, not a replacement.
const evalHeartbeatInterval = 25 * time.Millisecond

// EvalLoop runs the evaluation loop with whatever config is live when it
// starts. Kept for compatibility; StartEvalLoop calls evalLoop directly with
// the config captured BEFORE the goroutine is spawned.
func (c *Controller) EvalLoop() error {
	return c.evalLoop(c.GetConfig())
}

// evalLoop takes its starting config as an argument. W17 fork modification: the
// loop used to read c.Config from inside the new goroutine, which races any
// SetConfig running at the same time -- a real data race the race detector sees
// as soon as a test applies a config to a freshly built controller, and the
// reason pkg/server's load-path tests use a controller with no loop at all.
func (c *Controller) evalLoop(initial *Config) error {

	var config *Config
	//goland:noinspection GoPreferNilSlice
	holders := []*IOHolder{}

	// evalTransmitters is the shared body of the device-event branch and the
	// heartbeat: re-evaluate every synthetic per-port transmitter and let the
	// streams know. W17 fork addition (extracted, with the heartbeat).
	evalTransmitters := func() {
		for _, holder := range holders {
			if tx, ok := holder.IO.(*OutputTransmitter); ok {
				tx.Eval(holder.Config)
				c.alertEvalChan()
			}
		}
	}

	// W17 fork addition: the subscriber-independent heartbeat. See
	// evalHeartbeatInterval.
	heartbeat := time.NewTicker(evalHeartbeatInterval)
	defer heartbeat.Stop()

	EvalAll(initial)

Loop:
	for {
		select {
		case <-c.evalTomb.Dying():
			fmt.Println("(config): exiting eval loop")
			break Loop

		case event := <-c.ConfigEventChan:
			config = event.Config
			if config == nil {
				holders = []*IOHolder{}
				c.EvalDataMap = &map[string]*[16]util.CRSFValue{} //delete all existing entries
				c.EvalUnresolvedMap = &map[string]*atomic.Bool{}
				//W17 fork addition (MAP-4): clearing the config is an adoption
				//too -- SetConfig must not wait for a swap that will never come.
				event.adopted()
				continue
			}

			var published map[string]*[16]util.CRSFValue
			var unresolved map[string]*atomic.Bool
			holders, published, unresolved = applyConfig(config)

			//the maps are built by applyConfig and swapped in with a single
			//assignment each: the send loop reads these pointers from its own
			//goroutine and must never observe a half-filled map
			c.EvalDataMap = &published
			c.EvalUnresolvedMap = &unresolved

			//W17 fork addition (MAP-4): the config is LIVE from here -- this is
			//the swap the send loop reads. Release the caller who sent THIS
			//config, and only that caller (review finding B2): the done channel
			//came in on the event, so a second applier waiting on its own
			//channel is not released by this adoption. Reporting success for a
			//config that is not on the wire is the whole of MAP-4.
			event.adopted()
			c.alertEvalChan()
		case _ = <-c.StreamEventChan:
			if config == nil {
				continue
			}
			//evaluate all top-level configs, not just the transmitters
			for _, holder := range config.IOMap {
				holder.Eval(config)
				c.alertEvalChan()
			}
		case _ = <-c.deviceCtl.DeviceEventChan:
			evalTransmitters()
			//fmt.Printf("eval: %v\n", (*c.ChannelsDataMap)[sport.TX.Port])

		case <-heartbeat.C:
			//W17 fork addition: same work as a device event, unconditionally.
			//A removal whose alert was dropped -- or was consumed by a
			//competing receiver -- is picked up here within one interval.
			evalTransmitters()
		}
	}
	return nil

}

func (c *Controller) StartEvalLoop() error {

	if c.evalTomb != nil && c.evalTomb.Alive() {
		return errors.New("(config) eval loop already active")
	}

	// W17 fork modification: the event channels are created HERE, before the
	// goroutine is spawned, rather than inside EvalLoop. Anything that runs
	// after StartEvalLoop returns can then subscribe to EvalEventChan without
	// racing the loop's own startup -- previously the fields were written from
	// the new goroutine, so an early subscriber (a streaming RPC right after
	// boot, or a test) read them unsynchronized.
	c.initEvalChan()
	c.initStreamChan()

	// W17 fork modification: capture the starting config HERE, in the caller's
	// goroutine, rather than reading the field from inside the loop's.
	initial := c.GetConfig()

	c.evalTomb = &tomb.Tomb{}
	c.evalTomb.Go(func() error {
		return c.evalLoop(initial)
	})

	return nil
}

func (c *Controller) StopEvalLoop() error {
	if c.evalTomb == nil || !c.evalTomb.Alive() {
		return nil
	}

	c.evalTomb.Kill(nil)
	if err := c.evalTomb.Wait(); err != nil {
		return err
	}
	return nil
}
