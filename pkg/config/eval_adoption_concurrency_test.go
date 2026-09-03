// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// The adoption barrier under TWO appliers (review finding B2) -- the reviewer's
// reproduction, turned into a regression test.
//
// MAP-4's first fix gave the controller ONE shared barrier channel that the
// eval loop closed and replaced after every adoption, with SetConfig taking the
// current one before delivering. Under a single applier that is exactly right.
// Under two it is not: caller B takes barrier ch_n, A's adoption closes ch_n,
// and B -- delivering afterwards -- waits on an already-closed channel and
// returns SUCCESS for a config that has not been adopted. That is MAP-4's own
// failure mode surviving the fix for it, and FORK-NOTICE stated the guarantee
// unqualified.
//
// Race day has one applier. The editor's Apply is the second, and the two can
// overlap: the ground station launches the mapper headlessly while its web UI
// is still reachable from that PC.
//
// The fix under test: the done channel travels ON the event (ConfigEvent), so
// the loop closes the channel that arrived with the config it just published,
// and nothing else can close it.
//
// HOW EACH APPLIER CHECKS ITS OWN CONFIG. The published EvalDataMap is global
// -- a later adoption replaces it wholesale -- so it cannot answer "was MY
// config adopted?". The config's own transmitter node can: applyConfig runs
// EvalAll over the caller's OWN IOMap, and OutputTransmitter.Eval allocates
// Values (nil until then) and writes the channel value into it. Nothing else in
// this test touches that array -- the heartbeat re-evaluates the loop's
// SYNTHETIC holders, which are freshly built per adoption. So a non-nil Values
// carrying this caller's own value means this caller's config was evaluated by
// the loop, and the close/receive on Done is the happens-before edge that makes
// reading it sound.

import (
	"sync"
	"testing"
	"time"

	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// adoptionTestConfigOn is adoptionTestConfig with the port named, so two
// concurrent appliers cannot be confused for one another.
func adoptionTestConfigOn(ctl *Controller, port string, value util.RawValue) *Config {
	cfg := &Config{IOMap: map[string]*IOHolder{}}
	cfg.Ctl = ctl

	list := []*IOHolder{numberChannel(1, value)}
	cfg.IOMap["tx"] = &IOHolder{
		IO: &OutputTransmitter{
			Id: "tx", Type: "tx",
			Transmitter: TransmitterT{Port: port, Channels: &list},
		},
		Ctl: ctl, Config: cfg,
	}
	return cfg
}

// TestSetConfigBarrierIsPerCaller is the B2 gate. Two appliers hammer the same
// controller with their own configs; each asserts, the instant ITS SetConfig
// returns, that ITS config has been evaluated by the eval loop.
//
// Under the shared barrier this fails with "SetConfig returned before this
// caller's own config was live" (verified against the previous implementation).
func TestSetConfigBarrierIsPerCaller(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	const rounds = 200

	appliers := []struct {
		name  string
		port  string
		value util.RawValue
		want  util.CRSFValue
	}{
		{"A", "/dev/adoption-A", util.MaxRaw, full},
		{"B", "/dev/adoption-B", util.MinRaw, util.CRSFValue(util.CRSFMinValue)},
	}

	begin := make(chan struct{})
	var wg sync.WaitGroup

	for _, applier := range appliers {
		applier := applier
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin

			for round := 0; round < rounds; round++ {
				cfg := adoptionTestConfigOn(ctl, applier.port, applier.value)

				ctl.SetConfig(cfg)

				tx, ok := cfg.IOMap["tx"].IO.(*OutputTransmitter)
				if !ok {
					t.Errorf("%s: setup lost its transmitter node", applier.name)
					return
				}
				if tx.Values == nil {
					t.Errorf("%s round %d: SetConfig returned before this caller's own "+
						"config was live -- the barrier is not per caller",
						applier.name, round)
					return
				}
				if got := (*tx.Values)[0]; got != applier.want {
					t.Errorf("%s round %d: ch1 = %d, want %d -- this caller's config was "+
						"published but not evaluated", applier.name, round, got, applier.want)
					return
				}
			}
		}()
	}

	close(begin)

	// A wedged barrier would hang rather than fail, so bound the whole thing.
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()

	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent appliers did not finish: the adoption handshake is wedged")
	}
}

// TestTheLoopClosesTheDoneItWasGiven is the mechanism, stated directly and
// without goroutine timing: whatever channel arrives on the event is the
// channel the loop closes.
func TestTheLoopClosesTheDoneItWasGiven(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	mine := make(chan struct{})
	ctl.ConfigEventChan <- ConfigEvent{
		Config: adoptionTestConfigOn(ctl, "/dev/adoption-mechanism", util.MaxRaw),
		Done:   mine,
	}

	select {
	case <-mine:
	case <-time.After(2 * time.Second):
		t.Fatal("the eval loop did not close the done channel that came with the config")
	}

	if ctl.EvalDataMap == nil {
		t.Fatal("done closed before anything was published")
	}
	if _, ok := (*ctl.EvalDataMap)["/dev/adoption-mechanism"]; !ok {
		t.Errorf("done closed before the port was published; have %v", *ctl.EvalDataMap)
	}
}

// TestAnEventWithNoDoneSurvivesTheLoop keeps the loop usable by a producer
// that wants no acknowledgement -- and, more to the point, keeps a nil Done
// from panicking the eval goroutine, where a panic is a PROCESS fault.
//
// The proof that the loop survived is the SetConfig after it: that call has a
// done channel of its own, and it returns only when the loop closes it. The
// published map is read afterwards, through that same close, which is what
// makes the read sound -- EvalDataMap has no lock of its own and polling it
// from a test goroutine is a data race, not a check.
func TestAnEventWithNoDoneSurvivesTheLoop(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	ctl.ConfigEventChan <- ConfigEvent{
		Config: adoptionTestConfigOn(ctl, "/dev/adoption-nodone", util.MaxRaw),
	}

	done := make(chan struct{})
	go func() {
		ctl.SetConfig(adoptionTestConfigOn(ctl, "/dev/adoption-after-nodone", util.MaxRaw))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the eval loop stopped adopting after an event with no done channel")
	}

	if ctl.EvalDataMap == nil {
		t.Fatal("nothing was published")
	}
	if _, ok := (*ctl.EvalDataMap)["/dev/adoption-after-nodone"]; !ok {
		t.Errorf("the follow-up config was not published; have %v", *ctl.EvalDataMap)
	}
}

// TestClearingTheConfigReleasesItsOwnCaller keeps the nil-config branch honest:
// it is an adoption too, and it must close the done that arrived with it rather
// than leaving that caller waiting out the timeout.
func TestClearingTheConfigReleasesItsOwnCaller(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	ctl.SetConfig(adoptionTestConfigOn(ctl, "/dev/adoption-clear", util.MaxRaw))

	cleared := make(chan struct{})
	ctl.ConfigEventChan <- ConfigEvent{Config: nil, Done: cleared}

	select {
	case <-cleared:
	case <-time.After(2 * time.Second):
		t.Fatal("clearing the config never released its caller")
	}

	if ctl.EvalDataMap == nil || len(*ctl.EvalDataMap) != 0 {
		t.Errorf("clearing must publish an empty map; got %v", ctl.EvalDataMap)
	}
}
