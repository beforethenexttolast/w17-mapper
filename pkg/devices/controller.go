// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package devices

import (
	"fmt"
	"sync"
	"time"

	"github.com/dlsniper/debugger"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"github.com/veandco/go-sdl2/sdl"
	"gopkg.in/tomb.v2"
)

type Controller struct {
	// Gamepads is the device registry, keyed by the config-level device id
	// (DeriveGamepadId).
	//
	// W17 fork modification (MAP-6): it is now MUTATED at run time -- the poll
	// goroutine adds and removes entries as devices come and go -- while the
	// eval loop and the gRPC handlers read it. It is guarded by mu. READ IT
	// THROUGH Gamepad or GamepadList, never by ranging or indexing the field,
	// from anything outside this package. The field stays exported and stays a
	// plain map because single-goroutine tests across the repo build registries
	// as struct literals, where there is nothing to race with.
	Gamepads map[string]*InputGamepad

	// mu guards Gamepads. W17 fork addition (MAP-6).
	mu sync.RWMutex

	t                *tomb.Tomb
	DeviceEventCount int32
	DeviceEventChan  chan int32

	// pump and opener are the two SDL seams the hot-plug tests replace; nil
	// means the real SDL queue and SDL_JoystickOpen. See hotplug.go.
	pump   eventPump
	opener joystickOpener
}

func NewCtl() *Controller {
	devicesCtl := &Controller{}
	err := devicesCtl.Init()

	if err != nil {
		devicesCtl.Quit()
		panic(err)
	}

	return devicesCtl
}

// Gamepad resolves one device by its config-level id. W17 fork modification
// (MAP-6): read under the lock, because the poll goroutine can be inserting or
// deleting entries at the same instant.
func (c *Controller) Gamepad(id string) (*InputGamepad, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	res, ok := c.Gamepads[id]
	return res, ok
}

// GamepadList is a snapshot of the registry for callers that want all of it.
// W17 fork addition (MAP-6): the GetGamepads RPC used to range over the map
// field directly, which is a data race now that the map changes at run time.
// The slice is a copy; the *InputGamepad values in it are the live entries.
func (c *Controller) GamepadList() []*InputGamepad {
	c.mu.RLock()
	defer c.mu.RUnlock()

	list := make([]*InputGamepad, 0, len(c.Gamepads))
	for _, device := range c.Gamepads {
		list = append(list, device)
	}
	return list
}

func (c *Controller) Init() (err error) {
	if err = sdl.Init(sdl.INIT_GAMECONTROLLER); err != nil {
		return err
	}
	c.mu.Lock()
	c.Gamepads = enumerateDevices(c.joystickOpener())
	c.mu.Unlock()

	c.StartPolling()
	return err
}

func (c *Controller) Quit() {
	if err := c.StopPolling(); err != nil {
		fmt.Printf("error stopping polling loop. %s", err.Error())
	}

	for _, device := range c.GamepadList() {
		device.Close()
	}
	sdl.Quit()
}

func (c *Controller) GetGamepadStates(device *InputGamepad, states *pb.GamepadInputsStates) *pb.GamepadInputsStates {

	axisNumber := 0
	axesCount := int(device.Axes())
	buttonNumber := 0
	buttonsCount := int(device.Buttons())

	if states != nil {
		for axisNumber = 0; axisNumber < axesCount; axisNumber++ {
			states.InputsStates[axisNumber].Value = int32(device.Axis(axisNumber))
		}

		for buttonNumber = 0; buttonNumber < buttonsCount; buttonNumber++ {
			states.InputsStates[axesCount+buttonNumber].Value = int32(device.Button(buttonNumber))
		}

		return states
	}

	inputStates := make([]*pb.GamepadInputState, axesCount+buttonsCount)

	for axisNumber = 0; axisNumber < axesCount; axisNumber++ {
		inputStates[axisNumber] = &pb.GamepadInputState{
			Type:  pb.GamepadInputType_AXIS,
			Index: int32(axisNumber),
			Value: int32(device.Axis(axisNumber)),
		}
	}

	for buttonNumber = 0; buttonNumber < buttonsCount; buttonNumber++ {
		inputStates[axesCount+buttonNumber] = &pb.GamepadInputState{
			Type:  pb.GamepadInputType_BUTTON,
			Index: int32(buttonNumber),
			Value: int32(device.Button(buttonNumber)),
		}
	}

	states = &pb.GamepadInputsStates{InputsStates: inputStates}

	return states
}

func (c *Controller) initDeviceChan() {
	c.DeviceEventCount = 0
	c.DeviceEventChan = make(chan int32)
}

// AlertDeviceChan is a LOSSY wake-up, not a delivery guarantee: the send is
// non-blocking on an unbuffered channel, and the eval loop competes with any
// GetGamepadStream RPC for the receive, so any given alert -- including the
// single burst a device REMOVAL produces -- can be dropped outright or consumed
// by a subscriber that does not re-evaluate the transmitters. W17 fork note
// (2026-08-16 audit, defect 2): safety must therefore never depend on an alert
// arriving. It does not -- the config eval loop re-evaluates every transmitter
// on its own heartbeat (config.evalHeartbeatInterval), alerts merely lower the
// latency when they do get through.
func (c *Controller) AlertDeviceChan() {
	c.DeviceEventCount += 1 //it's okay if it overflows
	select {
	case c.DeviceEventChan <- c.DeviceEventCount:
		//fmt.Printf("event %d sent", c.EventCount)
	default:
		//no-op
	}
}

// pollIdleInterval is how long the poll loop waits when SDL's event queue is
// empty. W17 fork addition (review finding lifecycle-concurrency-7).
//
// Upstream's loop was `for { select { case <-Dying: ...; default: PollEvent() } }`
// with nothing in the default branch when PollEvent returned nil: one CPU core
// pegged at 100 % for the whole session, with no gamepad attached and no link
// running. On the giftee's laptop that is fan noise and battery for the length
// of a drive, in a process whose real work is a few hundred events a second.
//
// 1 ms costs nothing that matters. The events this loop drains are hot-plug
// notices and the wake-up pulse for the eval loop, and the eval loop already
// re-evaluates every transmitter on its own 25 ms heartbeat
// (config.evalHeartbeatInterval) whether a pulse arrives or not -- so the
// latency this can add is bounded by one poll interval on a path that is
// explicitly not allowed to depend on it (see AlertDeviceChan). When events ARE
// queued the loop drains them back-to-back and never reaches the wait.
const pollIdleInterval = time.Millisecond

func (c *Controller) StartPolling() {

	sdl.JoystickEventState(sdl.ENABLE)

	c.t = &tomb.Tomb{}
	c.initDeviceChan()
	c.t.Go(c.pollLoop)
}

// pollLoop drains the SDL event queue: hot-plug events update the registry
// (hotplug.go), and every event is still a wake-up pulse for the eval loop.
//
// It is separated from StartPolling so the hot-plug tests can run the real loop
// without SDL's global joystick event state, which they must not touch.
func (c *Controller) pollLoop() error {
	debugger.SetLabels(func() []string {
		return []string{
			"poller",
		}
	})

	pump := c.eventPump()

	for {
		select {
		case <-c.t.Dying():
			fmt.Println("(devices): exiting polling loop")
			return nil
		default:
		}

		if event := pump.Poll(); event != nil {
			c.handleDeviceEvent(event)
			c.AlertDeviceChan()
			continue
		}

		// Queue empty: wait rather than spin. Still tomb-armed, so a stop is
		// never delayed by more than one interval.
		select {
		case <-c.t.Dying():
			fmt.Println("(devices): exiting polling loop")
			return nil
		case <-time.After(pollIdleInterval):
		}
	}
}

func (c *Controller) StopPolling() error {
	sdl.JoystickEventState(sdl.DISABLE)
	return c.stopPollLoop()
}

// stopPollLoop kills the poll goroutine and waits for it. Separated from
// StopPolling for the same reason pollLoop is separated from StartPolling: the
// hot-plug tests run the real loop and must not touch SDL's global event state.
func (c *Controller) stopPollLoop() error {
	if c.t == nil {
		return nil
	}

	c.t.Kill(nil)
	if err := c.t.Wait(); err != nil {
		return err
	}
	return nil
}
