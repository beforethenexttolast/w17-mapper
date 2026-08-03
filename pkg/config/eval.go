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
func applyConfig(config *Config) ([]*IOHolder, map[string]*[16]util.CRSFValue) {
	holders := maps.Values(config.GetTransmitters())

	EvalAll(config)

	published := map[string]*[16]util.CRSFValue{}
	for _, holder := range holders {
		tx, ok := holder.IO.(*OutputTransmitter)
		if !ok {
			continue
		}
		tx.Eval(holder.Config)
		published[tx.Transmitter.Port] = tx.Values
	}

	return holders, published
}

func (c *Controller) EvalLoop() error {

	c.initEvalChan()
	c.initStreamChan()

	var config *Config
	//goland:noinspection GoPreferNilSlice
	holders := []*IOHolder{}

	EvalAll(c.Config)

Loop:
	for {
		select {
		case <-c.evalTomb.Dying():
			fmt.Println("(config): exiting eval loop")
			break Loop

		case config = <-c.ConfigEventChan:
			if config == nil {
				holders = []*IOHolder{}
				c.EvalDataMap = &map[string]*[16]util.CRSFValue{} //delete all existing entries
				continue
			}

			var published map[string]*[16]util.CRSFValue
			holders, published = applyConfig(config)

			//the map is built by applyConfig and swapped in with a single
			//assignment: the send loop reads this pointer from its own
			//goroutine and must never observe a half-filled map
			c.EvalDataMap = &published
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
			for _, holder := range holders {
				if tx, ok := holder.IO.(*OutputTransmitter); ok {
					tx.Eval(holder.Config)
					c.alertEvalChan()
				}
				//fmt.Printf("eval: %v\n", (*c.ChannelsDataMap)[sport.TX.Port])
			}
		}
	}
	return nil

}

func (c *Controller) StartEvalLoop() error {

	if c.evalTomb != nil && c.evalTomb.Alive() {
		return errors.New("(config) eval loop already active")
	}

	c.evalTomb = &tomb.Tomb{}
	c.evalTomb.Go(func() error {
		return c.EvalLoop()
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
