// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package devices

// Gamepad hot-plug: a pad that drops and comes back is seen again.
// W17 fork addition (review finding MAP-6, gift blocker tier C).
//
// WHAT WAS WRONG. The registry was built exactly once, by Controller.Init, and
// the poll loop threw every SDL event body away -- it read PollEvent purely as
// a "something happened" pulse for the eval loop. So:
//
//   - a pad switched on after the mapper started never appeared at all;
//   - a pad that dropped and reconnected -- which a DS4 does on a battery dip,
//     a knocked cable or a Bluetooth stutter -- never resolved again, because
//     its map entry still held the DEAD SDL handle from before the unplug.
//
// The car was safe throughout (an unresolvable or detached device drives the
// nan path, which the eval layer takes to a defined neutral, and the arm chain's
// reset_on_nan demands a fresh TRIANGLE afterwards) but the drive was over:
// nothing short of restarting the drive program brought the controller back.
// The printed remedy in the booklet, "reconnect the controller, then do the
// two-step", could not work. Handling the two device events is what makes that
// sentence true.
//
// WHAT IT DOES. On JOYDEVICEADDED the device index is opened and inserted under
// the id derived from its GUID and name (DeriveGamepadId), which is the same id
// the profile names and the same id it had before the unplug. On
// JOYDEVICEREMOVED the entry whose SDL instance id matches is closed and
// dropped, so resolution fails cleanly instead of reading frozen values off a
// detached handle.
//
// WHAT IT DELIBERATELY DOES NOT DO.
//
//   - It does not handle CONTROLLERDEVICEADDED/REMOVED. SDL raises those
//     ALONGSIDE the joystick events for the same physical device, and this
//     registry is joystick-based (JoystickOpen, not GameControllerOpen), so
//     handling both would mean opening the same device twice per plug-in and
//     reasoning about SDL's open refcount for no gain.
//   - It does not re-arm anything. reset_on_nan already requires a fresh press
//     after the gap (pkg/config/input_seq_reset_test.go), and hot-plug does not
//     weaken that: the gap still happened, so the toggle still reset.

import (
	"fmt"

	"github.com/veandco/go-sdl2/sdl"
)

// eventPump is the SDL event queue the poll loop drains. Production is
// sdlPump; the hot-plug tests inject a fake, because no unattended session may
// plug a gamepad in and out on demand.
type eventPump interface {
	Poll() sdl.Event
}

// sdlPump is the production eventPump.
type sdlPump struct{}

func (sdlPump) Poll() sdl.Event { return sdl.PollEvent() }

// joystickOpener opens the joystick at an SDL DEVICE INDEX. Production is
// sdlOpener (pkg/devices/util.go).
type joystickOpener interface {
	Open(deviceIndex int) (*InputGamepad, bool)
}

// eventPump returns the injected pump, or the real SDL queue.
func (c *Controller) eventPump() eventPump {
	if c.pump != nil {
		return c.pump
	}
	return sdlPump{}
}

// joystickOpener returns the injected opener, or the real SDL one.
func (c *Controller) joystickOpener() joystickOpener {
	if c.opener != nil {
		return c.opener
	}
	return sdlOpener{}
}

// handleDeviceEvent applies one SDL event to the registry. Everything that is
// not an add or a remove is ignored, exactly as before -- the caller still
// treats every event as a wake-up pulse.
func (c *Controller) handleDeviceEvent(event sdl.Event) {
	switch e := event.(type) {
	case *sdl.JoyDeviceAddedEvent:
		// For ADDED, Which is a DEVICE INDEX, not an instance id.
		c.deviceAdded(int(e.Which))
	case *sdl.JoyDeviceRemovedEvent:
		// For REMOVED, Which is the INSTANCE ID of the device that left.
		c.deviceRemoved(int32(e.Which))
	}
}

// deviceAdded opens the joystick at deviceIndex and puts it in the registry.
func (c *Controller) deviceAdded(deviceIndex int) {
	device, ok := c.joystickOpener().Open(deviceIndex)
	if !ok {
		fmt.Printf("(devices): a gamepad was plugged in at index %d but could not be opened\n", deviceIndex)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Gamepads == nil {
		c.Gamepads = map[string]*InputGamepad{}
	}

	// A duplicate ADDED for a device already registered: keep the handle that
	// is already in use and surrender the second one, so SDL's open refcount
	// stays balanced and no live binding is swapped out underneath the eval
	// loop.
	instance := device.InstanceId()
	for _, known := range c.Gamepads {
		if known.InstanceId() == instance && instance >= 0 {
			device.Close()
			return
		}
	}

	// Same collision rule as start-up enumeration: a second physically distinct
	// device that derives the same id gets the index appended. The FIRST holder
	// of an id keeps it, which is what makes a reconnect resolve again -- the id
	// was freed when the pad was removed.
	//
	// LIMIT, with TWO IDENTICAL pads attached (independent review, 2026-09-04):
	// "first holder keeps it" holds only for as long as that holder is
	// attached, and the id is a function of GUID and name, so two identical
	// pads derive the same one. Unplug pad A and the bare id is free; unplug
	// and replug pad B, registered as <id>_<n>, and B claims the bare id --
	// which is the id the profile names. The W17 rig is single-pad, so this
	// cannot arise on race day; a rig with two identical pads should not rely
	// on which physical pad a bare id lands on after a hot-plug.
	id := device.Id
	if existing, taken := c.Gamepads[id]; taken && existing != device {
		id = fmt.Sprintf("%s_%d", id, deviceIndex)
		device.Id = id
	}

	c.Gamepads[id] = device
	fmt.Printf("(devices): gamepad connected: %s (id %s)\n", device.Name, id)
}

// deviceRemoved drops the entry whose SDL instance id matches, and RETIRES its
// handle rather than closing it.
//
// Why it is not closed here. Every reader takes the registry lock only long
// enough to fetch the *InputGamepad, then reads through it unlocked -- the eval
// path does exactly that (config.GetInputGamepad -> Attached, then Axis/Button
// on the way through the node graph), and the GetGamepadStream RPC holds the
// same pointer for the whole life of the stream and reads axes every 25 ms.
// Closing here frees the SDL_Joystick underneath all of them: SDL validates the
// pointer before using it, so this is not a crash so much as a read of freed
// memory, but it is a genuine use-after-free and the poll goroutine would be
// creating it at an arbitrary instant, mid-drive, on a physical unplug.
//
// The cost of retiring instead is one SDL_Joystick object -- and the OS device
// handle behind it -- per unplug, held until Quit. That is a bounded leak on a
// path that happens a handful of times in a session, and it buys the guarantee
// that a pointer already handed out stays valid: reads on it report the frozen
// last values and Attached() reports FALSE, which is the state device.go's
// Attached() exists to describe and which the eval path already neutralizes.
//
// Dropping the map entry is what actually matters, and it happens immediately:
// the id stops resolving, so nothing NEW can reach the dead device, and the
// same id is free for the pad when it comes back.
func (c *Controller) deviceRemoved(instance int32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, device := range c.Gamepads {
		if device.InstanceId() != instance {
			continue
		}

		delete(c.Gamepads, id)
		c.retired = append(c.retired, device)
		fmt.Printf("(devices): gamepad disconnected: %s (id %s)\n", device.Name, id)
		return
	}
}
