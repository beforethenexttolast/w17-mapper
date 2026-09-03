// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"github.com/kaack/elrs-joystick-control/pkg/util"
	"gopkg.in/tomb.v2"
	"sync"
	"sync/atomic"
	"time"
)

type Controller struct {
	Config *Config `json:"config"`

	deviceCtl   *dc.Controller
	EvalDataMap *map[string]*[16]util.CRSFValue `json:"-"`

	// EvalUnresolvedMap is the per-port "do not transmit" flag that goes with
	// EvalDataMap, published by the same applyConfig call and swapped in the same
	// place. W17 fork addition.
	//
	// A port's flag is set when OutputTransmitter.Eval could not account for
	// every channel the transmitter drives, which today means only one thing:
	// the channelOwners walk hit channelOwnerMaxDepth, so the owner set is
	// incomplete and some channel may be holding a value nothing can neutralize.
	// The send loop suppresses that port's channel frames while it is set, the
	// same answer resolveChannels and configSwapGate already give to unknown
	// state.
	//
	// Nil until a config is applied, which reads as "not unresolved" -- the
	// no-config case is already covered by EvalDataMap having no entry.
	EvalUnresolvedMap *map[string]*atomic.Bool `json:"-"`

	// EvalNoData is the placeholder reported for a port with no config.
	//
	// W17 fork modification: this is a DISPLAY value only -- it feeds
	// GetTransmitterChannels, which backs the UI's channel readout. It must
	// never be transmitted. The send loop deliberately writes no channel frame
	// at all when no config resolves, so that the receiver's link-loss failsafe
	// can fire; see link.resolveChannels for the full reasoning.
	//
	// The former EvalCenter (all 992) was removed rather than adopted as the
	// transmitted value: center sits inside a receiver's switch hysteresis
	// band, so it HOLDS the previous switch state and would leave an arm switch
	// latched ON when a config is cleared mid-session.
	EvalNoData *[16]util.CRSFValue

	//channelsTomb      *tomb.Tomb
	//ChannelEventCount int32
	//ChannelEventChan  chan int32

	evalTomb       *tomb.Tomb
	EvalEventCount int32
	EvalEventChan  chan int32

	StreamEventCount int32
	StreamEventChan  chan int32

	// ConfigEventChan carries a newly applied config to the eval loop, which is
	// the SOLE adoption point: it rebuilds the transmitter holders and swaps in
	// the published channel arrays (see EvalLoop).
	//
	// W17 fork modification (MAP-4): BUFFERED (1). It used to be unbuffered and
	// written with a non-blocking send, so with the loop momentarily busy the
	// alert was simply dropped -- and SetConfig returned success anyway. The
	// config was then live in c.Config, visible to the editor and to GetConfig,
	// while the send loop went on transmitting the PREVIOUS config's arrays,
	// with nothing to correct it: unlike the sibling device alert (whose dropped
	// events the 25 ms heartbeat re-picks up), a dropped config alert has no
	// second chance -- the heartbeat re-evaluates the OLD holders.
	ConfigEventChan chan *Config

	// configAdopted is closed by the eval loop each time it finishes publishing
	// a config, and replaced with a fresh channel. SetConfig takes the current
	// one before it delivers, so waiting on it means "the config that is live
	// now is mine or newer". W17 fork addition (MAP-4).
	adoptionMu    sync.Mutex
	configAdopted chan struct{}
}

func NewCtl(dc *dc.Controller) *Controller {
	configCtl := &Controller{
		deviceCtl:  dc,
		EvalNoData: &[16]util.CRSFValue{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	err := configCtl.Init()

	if err != nil {
		configCtl.Quit()
		panic(err)
	}

	return configCtl
}

func (c *Controller) Init() error {
	//if err := c.StartChannelsLoop(); err != nil {
	//	return err
	//}

	c.initConfigChan()

	if err := c.StartEvalLoop(); err != nil {
		return err
	}

	return nil
}

func (c *Controller) Quit() {
	//if err := c.StopChannelsLoop(); err != nil {
	//	fmt.Printf("error stopping channels loop. %s\n", err.Error())
	//}

	if err := c.StopEvalLoop(); err != nil {
		fmt.Printf("error halting eval loop. %s\n", err.Error())
	}
}

func (c *Controller) UnmarshalJSON(configJson []byte) error {
	var err error

	var rawData json.RawMessage
	if err = json.Unmarshal(configJson, &rawData); err != nil {
		return errors.New(err.Error())
	}

	var tmp struct {
		Config *Config `json:"config"`
	}

	if err = json.Unmarshal(rawData, &tmp); err != nil {
		return errors.New(err.Error())
	}

	c.Config = tmp.Config

	//propagate the config controller to all children
	c.Config.Ctl = c

	return nil
}

func (c *Controller) GetTransmitterChannels(device *pb.Transmitter, channels *pb.TransmitterChannels) *pb.TransmitterChannels {

	var ok bool
	var channelsData *[16]util.CRSFValue

	if c.EvalDataMap == nil {
		channelsData = c.EvalNoData
	} else if channelsData, ok = (*c.EvalDataMap)[device.Port]; !ok {
		channelsData = c.EvalNoData
	}

	if channels == nil {
		channels = &pb.TransmitterChannels{
			Channels: make([]*pb.TransmitterChannel, 16),
		}

		for i := 0; i < 16; i++ {
			channels.Channels[i] = &pb.TransmitterChannel{
				ChannelNumber: int32(i) + 1,
				ChannelValue:  int32(channelsData[i]),
			}
		}

		return channels
	}

	for i := 0; i < 16; i++ {
		channels.Channels[i].ChannelValue = int32(channelsData[i])
	}

	return channels
}

func setStates(c *Config, states *map[string]*pb.EvalState, ih *IOHolder) {
	if ih == nil || states == nil {
		return
	}

	id := ih.InputId()

	//evaluate node unconditionally
	//this makes easier to troubleshoot when viewing values in the editor
	ih.Eval(c)
	if (*states)[id] == nil {
		(*states)[id] = &pb.EvalState{}
	}

	value := ih.InputValue()
	if value == nil {
		(*states)[ih.InputId()].IsNaN = true
		(*states)[ih.InputId()].Value = 0
	} else {
		(*states)[ih.InputId()].IsNaN = false
		(*states)[ih.InputId()].Value = int32(*value)
	}

	children := ih.Children()
	if children != nil {
		for _, ih := range *children {
			setStates(c, states, ih)
		}
	}
}

func (c *Controller) GetEvalStates(states *pb.EvalStates) *pb.EvalStates {
	if states == nil {
		states = &pb.EvalStates{
			States: map[string]*pb.EvalState{},
		}
	}

	config := c.Config
	if config == nil {
		return states
	}

	for _, ih := range config.IOMap {
		setStates(c.Config, &states.States, ih)
	}

	return states
}

// configAdoptionTimeout bounds each of the two waits in SetConfig. The eval
// loop adopts a config in one loop iteration, so this is three orders of
// magnitude of headroom; it exists only so that a wedged or dead loop turns
// into a loud warning instead of a hung RPC. W17 fork addition (MAP-4).
const configAdoptionTimeout = 2 * time.Second

// SetConfig makes config the live config and returns once the eval loop has
// ADOPTED it -- that is, once the transmitter holders have been rebuilt and the
// published channel arrays swapped in, which is the moment the send loop starts
// transmitting from it.
//
// W17 fork modification (MAP-4). Upstream set the field, fired a droppable
// non-blocking alert and returned success. When that alert was dropped the RPC
// still reported success, the editor showed the new config as applied, and the
// car went on being driven by the OLD one, indefinitely: the eval loop's 25 ms
// heartbeat re-evaluates the holders it already has, so it repairs a dropped
// DEVICE alert but can never repair a dropped CONFIG alert. On the race-day
// path this is the first thing that happens after launch, so the failure mode
// was "the profile loaded" with the profile not loaded.
//
// A controller with no running eval loop -- a bare one in a load-path test, or
// one already shut down -- has nothing that can adopt anything, so it keeps the
// old shape: set the field and return.
func (c *Controller) SetConfig(config *Config) {
	config.Ctl = c
	c.Config = config

	if c.ConfigEventChan == nil || c.evalTomb == nil || !c.evalTomb.Alive() {
		return
	}

	// Taken BEFORE the delivery, so the close we wait for cannot be one that
	// happened before our config was sent.
	adopted := c.adoptionBarrier()

	select {
	case c.ConfigEventChan <- config:
	case <-c.evalTomb.Dying():
		return
	case <-time.After(configAdoptionTimeout):
		fmt.Printf("(config) WARNING: the eval loop did not take the new config within %v -- "+
			"it is NOT live and the previous one is still being transmitted\n", configAdoptionTimeout)
		return
	}

	select {
	case <-adopted:
	case <-c.evalTomb.Dying():
	case <-time.After(configAdoptionTimeout):
		fmt.Printf("(config) WARNING: the new config was delivered but not adopted within %v -- "+
			"it may not be on the wire yet\n", configAdoptionTimeout)
	}
}

// adoptionBarrier returns the channel the NEXT adoption will close.
func (c *Controller) adoptionBarrier() <-chan struct{} {
	c.adoptionMu.Lock()
	defer c.adoptionMu.Unlock()

	if c.configAdopted == nil {
		c.configAdopted = make(chan struct{})
	}
	return c.configAdopted
}

// markAdopted is called by the eval loop once a config's channel arrays are
// published, releasing whoever is waiting in SetConfig. W17 fork addition.
func (c *Controller) markAdopted() {
	c.adoptionMu.Lock()
	defer c.adoptionMu.Unlock()

	if c.configAdopted != nil {
		close(c.configAdopted)
	}
	c.configAdopted = make(chan struct{})
}

func (c *Controller) initConfigChan() {
	// Buffered so delivery does not depend on the loop being at its select the
	// instant SetConfig runs; see ConfigEventChan.
	c.ConfigEventChan = make(chan *Config, 1)

	c.adoptionMu.Lock()
	defer c.adoptionMu.Unlock()
	c.configAdopted = make(chan struct{})
}
