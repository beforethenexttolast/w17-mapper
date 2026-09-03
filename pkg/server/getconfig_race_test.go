// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

// The GetConfig RPC against a concurrent apply (review finding N2).
//
// server_grpc.go's GetConfig marshals the config controller's live config from
// a gRPC worker goroutine. SetConfig writes that same field from whichever
// goroutine applied -- the editor's Apply, or the headless bring-up. The field
// was unsynchronized on both sides, and -race never noticed because nothing
// drove the two together. This does.
//
// The config controller here has no eval loop (see startSetConfigServer): with
// no loop nothing evaluates the node graph, so the only state the two
// goroutines share is the field itself, which is what the fix guards.

import (
	"context"
	"sync"
	"testing"

	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
)

func TestGetConfigRPCIsSafeAgainstAConcurrentSetConfig(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	const rounds = 200

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			configCtl.SetConfig(&cc.Config{IOMap: map[string]*cc.IOHolder{}})
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := client.GetConfig(context.Background(), &pb.Empty{}); err != nil {
				t.Errorf("GetConfig failed while a config was being applied: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}
