// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package config

// Tests for the Config field's synchronization (review finding N2).
//
// The defect: SetConfig WROTE Controller.Config while the gRPC GetConfig
// handler and GetEvalStates READ it bare, from other goroutines. That is a
// plain data race on the field, and -race never saw it because no test drove
// the two together -- so "the -race suite is green" was a statement about the
// tests that existed, not about the field.
//
// These drive them together. Without the lock they report
// "DATA RACE ... Write at ... by goroutine N: SetConfig / Previous read at ...".
//
// Scope, stated so it is not over-read: what is guarded is the POINTER. The
// node graph it leads to is evaluated in place by the eval loop, and anything
// that walks a LIVE config's nodes shares that pre-existing upstream hazard
// with the loop -- which is exactly why the reader below reads the pointer and
// why the deep-read test uses a controller with no loop.

import (
	"encoding/json"
	"sync"
	"testing"

	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

const configFieldRaceRounds = 200

// TestGetConfigIsSafeAgainstAConcurrentApply is the finding, on a LIVE
// controller: applies and reads racing on one field, exactly the shape a web
// editor Apply and a GetConfig poll produce.
func TestGetConfigIsSafeAgainstAConcurrentApply(t *testing.T) {
	ctl := NewCtl(&dc.Controller{Gamepads: map[string]*dc.InputGamepad{}})
	defer ctl.Quit()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < configFieldRaceRounds; i++ {
			ctl.SetConfig(adoptionTestConfig(ctl, util.MaxRaw))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < configFieldRaceRounds; i++ {
			if cfg := ctl.GetConfig(); cfg != nil && cfg.IOMap == nil {
				t.Error("GetConfig returned a config with no IOMap -- a torn read")
			}
		}
	}()

	wg.Wait()
}

// TestGetConfigSurvivesAConcurrentMarshal is the GetConfig RPC's own work --
// json.Marshal of the whole config -- run against a concurrent apply.
//
// The controller here has NO eval loop on purpose: nothing then evaluates the
// node graph, so the only thing the two goroutines share is the field itself,
// which is the thing under test. (With a live loop this would also exercise the
// pre-existing node-graph hazard described at the top, which is not this fix.)
func TestGetConfigSurvivesAConcurrentMarshal(t *testing.T) {
	bare := &Controller{}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < configFieldRaceRounds; i++ {
			bare.SetConfig(&Config{IOMap: map[string]*IOHolder{}})
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < configFieldRaceRounds; i++ {
			if _, err := json.Marshal(bare.GetConfig()); err != nil {
				t.Errorf("marshalling the live config failed: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}

// TestGetEvalStatesReadsTheGuardedField pins the other reader named in the
// finding. It is a behaviour check, not a race check: GetEvalStates must go
// through the accessor and answer from whatever config is live.
func TestGetEvalStatesReadsTheGuardedField(t *testing.T) {
	bare := &Controller{}

	if states := bare.GetEvalStates(nil); states == nil || len(states.States) != 0 {
		t.Errorf("with no config there are no eval states; got %v", states)
	}

	bare.SetConfig(adoptionTestConfig(bare, util.MaxRaw))
	if states := bare.GetEvalStates(nil); states == nil || len(states.States) == 0 {
		t.Errorf("with a config applied there must be eval states; got %v", states)
	}
}
