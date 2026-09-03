// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package devices

// Hot-plug tests (review finding MAP-6, and the poll spin,
// lifecycle-concurrency-7).
//
// The behaviour under test is the one the booklet promises Lola: unplug the
// controller, plug it back in, and the car answers the sticks again. Before
// this, the registry was enumerated once at boot and the poll loop discarded
// every event body, so a reconnected pad never resolved and the only cure was
// restarting the drive program.
//
// Nothing here touches SDL. An sdl.Joystick is an opaque C handle that only
// SDL_JoystickOpen can produce and no unattended session may plug a real pad in
// and out, so the two SDL seams -- the event queue and the opener -- are faked
// (see hotplug.go), and the pads are fakeJoystick values behind the Joystick
// interface. What is NOT covered here is SDL's own behaviour: that Windows
// HIDAPI raises JOYDEVICEADDED/REMOVED for a DS4 at all, and that the GUID it
// reports after a re-plug is byte-identical, are reasoned from SDL's documented
// semantics and remain [bench-TBD].

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"gopkg.in/tomb.v2"
)

// fakeJoystick is a Joystick with no SDL behind it.
type fakeJoystick struct {
	instance sdl.JoystickID
	attached bool
	closes   atomic.Int64
	axes     []int16
	buttons  []byte
}

func (f *fakeJoystick) Attached() bool             { return f.attached }
func (f *fakeJoystick) Axis(axis int) int16        { return f.axes[axis] }
func (f *fakeJoystick) Button(button int) byte     { return f.buttons[button] }
func (f *fakeJoystick) Hat(int) byte               { return sdl.HAT_CENTERED }
func (f *fakeJoystick) NumAxes() int               { return len(f.axes) }
func (f *fakeJoystick) NumButtons() int            { return len(f.buttons) }
func (f *fakeJoystick) NumHats() int               { return 0 }
func (f *fakeJoystick) InstanceID() sdl.JoystickID { return f.instance }
func (f *fakeJoystick) Close()                     { f.closes.Add(1); f.attached = false }

// The identity the profile names. Both halves feed DeriveGamepadId, and both are
// properties of the DEVICE, not of the connection -- which is the whole reason a
// reconnect can resolve.
const (
	padGUID = "030000004c050000cc09000000006800"
	padName = "PS4 Controller"
)

func padID() string { return DeriveGamepadId(padGUID, padName) }

// newFakePad is one plug-in: a fresh SDL handle with a fresh instance id, under
// the id the GUID and name derive.
func newFakePad(instance sdl.JoystickID) (*InputGamepad, *fakeJoystick) {
	joy := &fakeJoystick{
		instance: instance,
		attached: true,
		axes:     make([]int16, 6),
		buttons:  make([]byte, 16),
	}
	return &InputGamepad{Id: padID(), Name: padName, Joy: joy}, joy
}

// scriptedOpener answers Open with the next pad in its script.
type scriptedOpener struct {
	pads  []*InputGamepad
	opens int
}

func (o *scriptedOpener) Open(int) (*InputGamepad, bool) {
	if o.opens >= len(o.pads) {
		return nil, false
	}
	pad := o.pads[o.opens]
	o.opens++
	return pad, true
}

// scriptedPump hands out one queued event per Poll, then reports an empty queue
// forever -- which is what the real SDL queue does between plug events.
type scriptedPump struct {
	events []sdl.Event
	polls  atomic.Int64
	next   atomic.Int64
}

func (p *scriptedPump) Poll() sdl.Event {
	p.polls.Add(1)
	i := p.next.Load()
	if int(i) >= len(p.events) {
		return nil
	}
	p.next.Add(1)
	return p.events[i]
}

func added(deviceIndex int) sdl.Event {
	return &sdl.JoyDeviceAddedEvent{Type: sdl.JOYDEVICEADDED, Which: sdl.JoystickID(deviceIndex)}
}

func removed(instance sdl.JoystickID) sdl.Event {
	return &sdl.JoyDeviceRemovedEvent{Type: sdl.JOYDEVICEREMOVED, Which: instance}
}

// TestAReconnectedGamepadResolvesUnderTheSameId is MAP-6 itself: the pad goes
// away, the id stops resolving, the pad comes back, and the SAME id -- the one
// written into configs/w17-ds4.json -- resolves again, onto the NEW handle.
func TestAReconnectedGamepadResolvesUnderTheSameId(t *testing.T) {
	first, firstJoy := newFakePad(3)
	// SDL issues a fresh instance id on every plug-in; the derived id does not
	// change, because it is a function of the GUID and the name only.
	second, secondJoy := newFakePad(4)

	c := &Controller{
		Gamepads: map[string]*InputGamepad{first.Id: first},
		opener:   &scriptedOpener{pads: []*InputGamepad{second}},
	}

	c.handleDeviceEvent(removed(3))

	if _, ok := c.Gamepad(padID()); ok {
		t.Fatalf("the pad still resolves after JOYDEVICEREMOVED -- the eval path would "+
			"keep reading frozen values off a detached handle (id %s)", padID())
	}
	if got := firstJoy.closes.Load(); got != 1 {
		t.Errorf("the removed pad's SDL handle was closed %d times, want exactly 1", got)
	}

	c.handleDeviceEvent(added(0))

	back, ok := c.Gamepad(padID())
	if !ok {
		t.Fatalf("the reconnected pad does not resolve under %s -- this is MAP-6: the "+
			"drive is over until the mapper restarts, and the booklet's "+
			"'reconnect the controller' remedy cannot work", padID())
	}
	if back.Joy != secondJoy {
		t.Errorf("the registry kept a stale handle after the reconnect")
	}
	if !back.Attached() {
		t.Errorf("the reconnected pad reports detached, so every input node still resolves to nan")
	}
}

// TestARemovedPadLeavesTheRegistryEmpty pins the other half of the removal: not
// merely unresolvable, but gone, so nothing walks it and Quit does not close it
// twice.
func TestARemovedPadLeavesTheRegistryEmpty(t *testing.T) {
	pad, joy := newFakePad(9)
	c := &Controller{Gamepads: map[string]*InputGamepad{pad.Id: pad}}

	c.handleDeviceEvent(removed(9))

	if got := len(c.GamepadList()); got != 0 {
		t.Errorf("registry holds %d devices after the only one was removed, want 0", got)
	}
	if got := joy.closes.Load(); got != 1 {
		t.Errorf("close count %d, want 1", got)
	}
}

// TestARemovalForAnUnknownInstanceChangesNothing: SDL raises removals for
// devices this registry never opened (anything past the 16 the enumeration
// walks, or a device opened by another process's SDL context is not ours to
// touch). The handler must be a no-op there, not a panic and not a guess.
func TestARemovalForAnUnknownInstanceChangesNothing(t *testing.T) {
	pad, joy := newFakePad(1)
	c := &Controller{Gamepads: map[string]*InputGamepad{pad.Id: pad}}

	c.handleDeviceEvent(removed(42))

	if _, ok := c.Gamepad(padID()); !ok {
		t.Errorf("an unrelated removal dropped the wrong device")
	}
	if got := joy.closes.Load(); got != 0 {
		t.Errorf("an unrelated removal closed the wrong handle (%d closes)", got)
	}
}

// TestADuplicateAddedEventKeepsTheHandleInUse. SDL can raise a second ADDED for
// a device already open (a re-enumeration, or the controller event that shadows
// the joystick one). Swapping the live handle out from under the eval loop for
// an identical one is pointless and leaves SDL's open refcount unbalanced, so
// the second handle is surrendered.
func TestADuplicateAddedEventKeepsTheHandleInUse(t *testing.T) {
	first, firstJoy := newFakePad(5)
	duplicate, duplicateJoy := newFakePad(5)

	c := &Controller{
		Gamepads: map[string]*InputGamepad{first.Id: first},
		opener:   &scriptedOpener{pads: []*InputGamepad{duplicate}},
	}

	c.handleDeviceEvent(added(0))

	if got := len(c.GamepadList()); got != 1 {
		t.Fatalf("registry holds %d devices after a duplicate ADDED, want 1", got)
	}
	held, _ := c.Gamepad(padID())
	if held.Joy != firstJoy {
		t.Errorf("the duplicate ADDED replaced the handle already in use")
	}
	if got := duplicateJoy.closes.Load(); got != 1 {
		t.Errorf("the surplus handle was closed %d times, want 1 -- SDL's open refcount "+
			"is otherwise left one too high", got)
	}
}

// TestASecondDistinctPadWithTheSameDerivedIdGetsTheIndexSuffix keeps the
// hot-plug path on the same collision rule as start-up enumeration: two
// identical pads (same GUID, same name) derive the same id, and the second one
// to arrive is the one that gets renamed. The first keeps the plain id, which is
// what lets a reconnect land back on the profile's id.
func TestASecondDistinctPadWithTheSameDerivedIdGetsTheIndexSuffix(t *testing.T) {
	one, _ := newFakePad(11)
	two, _ := newFakePad(12)

	c := &Controller{opener: &scriptedOpener{pads: []*InputGamepad{one, two}}}

	c.handleDeviceEvent(added(0))
	c.handleDeviceEvent(added(1))

	if _, ok := c.Gamepad(padID()); !ok {
		t.Errorf("the first pad lost the plain id %s", padID())
	}
	if _, ok := c.Gamepad(padID() + "_1"); !ok {
		t.Errorf("the second pad was not registered as %s_1; registry now holds %d devices",
			padID(), len(c.GamepadList()))
	}
}

// TestAnAddedIndexThatCannotBeOpenedIsIgnored: SDL_JoystickOpen returns NULL for
// a device that vanished between the event and the open. Nothing is registered,
// nothing panics.
func TestAnAddedIndexThatCannotBeOpenedIsIgnored(t *testing.T) {
	c := &Controller{opener: &scriptedOpener{}}

	c.handleDeviceEvent(added(0))

	if got := len(c.GamepadList()); got != 0 {
		t.Errorf("registry holds %d devices after an unopenable ADDED, want 0", got)
	}
}

// TestANonDeviceEventChangesTheRegistry pins that everything else is still
// merely a wake-up pulse: axis motion, button presses and window events must not
// touch the registry.
func TestANonDeviceEventChangesTheRegistry(t *testing.T) {
	pad, _ := newFakePad(2)
	c := &Controller{Gamepads: map[string]*InputGamepad{pad.Id: pad}}

	c.handleDeviceEvent(&sdl.JoyAxisEvent{Type: sdl.JOYAXISMOTION, Which: 2, Axis: 0, Value: 100})

	if got := len(c.GamepadList()); got != 1 {
		t.Errorf("a JOYAXISMOTION event changed the registry (now %d devices)", got)
	}
}

// TestStartUpEnumerationAppendsTheIndexOnACollision pins upstream's start-up
// rule unchanged after the opener seam was introduced.
func TestStartUpEnumerationAppendsTheIndexOnACollision(t *testing.T) {
	one, _ := newFakePad(20)
	two, _ := newFakePad(21)

	registry := enumerateDevices(&scriptedOpener{pads: []*InputGamepad{one, two}})

	if len(registry) != 2 {
		t.Fatalf("enumerated %d devices, want 2", len(registry))
	}
	if _, ok := registry[padID()]; !ok {
		t.Errorf("the device at index 0 is not under the plain id")
	}
	if _, ok := registry[padID()+"_1"]; !ok {
		t.Errorf("the device at index 1 is not under the _1 suffix")
	}
}

// TestTheDerivedIdIgnoresTheConnectionButNotTheTransport is the documented
// property configs/README.md now states at the placeholder: the id survives an
// unplug (same GUID, same name) and does NOT survive a change of bus, because
// SDL encodes the bus in the GUID.
func TestTheDerivedIdIgnoresTheConnectionButNotTheTransport(t *testing.T) {
	if DeriveGamepadId(padGUID, padName) != DeriveGamepadId(padGUID, padName) {
		t.Fatal("the derivation is not a function of its inputs")
	}

	// Same pad, Bluetooth: SDL's GUID differs in the bus field.
	const bluetoothGUID = "050000004c050000cc09000000810000"
	if DeriveGamepadId(padGUID, padName) == DeriveGamepadId(bluetoothGUID, padName) {
		t.Error("USB and Bluetooth derive the same id -- configs/README.md's warning " +
			"that the placeholder must be filled per transport would be wrong")
	}

	if got := len(padID()); got != 6 {
		t.Errorf("id length %d, want 6 -- saved configs carry six hex digits", got)
	}
}

// --- the poll loop itself -------------------------------------------------

// TestThePollLoopWaitsWhenTheQueueIsEmpty is lifecycle-concurrency-7. Upstream's
// loop called PollEvent with nothing in the default branch, so it spun a core at
// 100 % for the whole session. The bound here is deliberately loose: at one poll
// per millisecond 60 ms allows ~60, and a spinning loop on this machine reaches
// the millions -- any number in between still means the wait is gone.
func TestThePollLoopWaitsWhenTheQueueIsEmpty(t *testing.T) {
	pump := &scriptedPump{}
	c := &Controller{pump: pump}
	c.t = &tomb.Tomb{}
	c.initDeviceChan()
	c.t.Go(c.pollLoop)

	time.Sleep(60 * time.Millisecond)

	if err := c.stopPollLoop(); err != nil {
		t.Fatalf("stopping the poll loop: %v", err)
	}

	polls := pump.polls.Load()
	if polls == 0 {
		t.Fatal("setup: the loop never polled at all")
	}
	t.Logf("%d polls in 60 ms of an empty queue", polls)
	const spinCeiling = 5000
	if polls > spinCeiling {
		t.Errorf("%d polls in 60 ms: the loop is spinning, not waiting (%v per poll). "+
			"That is one core at 100 %% for the whole drive on the giftee's laptop",
			polls, time.Duration(int64(60*time.Millisecond)/polls))
	}
}

// TestThePollLoopAppliesHotPlugEventsAndAlerts runs the REAL loop, not just the
// handler, so the wiring is pinned too: a queued ADDED reaches the registry, and
// the event is still raised as a wake-up for the eval loop.
//
// It asserts the alert was RAISED (DeviceEventCount), not that a subscriber
// received it. AlertDeviceChan is a lossy non-blocking send on an unbuffered
// channel by design -- see its comment -- so "a listener sees this pulse" is not
// a property of the code, and a test that asserted it would be asserting the
// scheduler again. The counter is read after stopPollLoop has joined the poll
// goroutine, so there is a happens-before edge and nothing to race with.
func TestThePollLoopAppliesHotPlugEventsAndAlerts(t *testing.T) {
	pad, _ := newFakePad(30)
	pump := &scriptedPump{events: []sdl.Event{added(0)}}

	c := &Controller{
		pump:   pump,
		opener: &scriptedOpener{pads: []*InputGamepad{pad}},
	}
	c.t = &tomb.Tomb{}
	c.initDeviceChan()
	c.t.Go(c.pollLoop)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := c.Gamepad(padID()); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if _, ok := c.Gamepad(padID()); !ok {
		t.Errorf("the poll loop did not apply a queued JOYDEVICEADDED within 2s")
	}

	if err := c.stopPollLoop(); err != nil {
		t.Fatalf("stopping the poll loop: %v", err)
	}

	if got := c.DeviceEventCount; got != 1 {
		t.Errorf("DeviceEventCount = %d after one queued event, want 1 -- the loop no "+
			"longer raises the eval loop's wake-up (its 25 ms heartbeat still covers "+
			"correctness, but the low-latency path is gone)", got)
	}
}

// TestStoppingTheLoopIsNotDelayedByTheWait: the idle wait is tomb-armed, so a
// stop costs at most one poll interval, not a whole shutdown timeout.
func TestStoppingTheLoopIsNotDelayedByTheWait(t *testing.T) {
	c := &Controller{pump: &scriptedPump{}}
	c.t = &tomb.Tomb{}
	c.initDeviceChan()
	c.t.Go(c.pollLoop)

	// Let it reach the wait.
	time.Sleep(5 * time.Millisecond)

	stopped := make(chan error, 1)
	start := time.Now()
	go func() { stopped <- c.stopPollLoop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("stopping the poll loop: %v", err)
		}
		if took := time.Since(start); took > 500*time.Millisecond {
			t.Errorf("the stop took %v -- the idle wait is not tomb-armed", took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the poll loop did not stop within 2s: the idle wait is not tomb-armed")
	}
}
