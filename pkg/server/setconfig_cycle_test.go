// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

// Load-path tests for SetConfig's read-cycle refusal.
//
// Every config load goes through this one RPC -- the web editor's Apply and
// the -config-file-path startup path alike -- so this is the choke point where
// a schema-valid `read` cycle must be refused BEFORE the config is applied.
// The runtime re-entrancy guard in pkg/config is the backstop; this is the
// front door. See pkg/config/read_cycles.go and input_read_cycle_test.go.

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

// startSetConfigServer serves a GRPCServer over an in-memory bufconn, with a
// config controller that has NO running eval loop: these tests are about the
// load path (validate -> refuse or apply), and the controller's SetConfig and
// alertConfigChan tolerate a bare controller by design (nil event channel,
// non-blocking send). Eval behaviour is covered in pkg/config's own tests --
// and a live loop here would trip the race detector on the pre-existing,
// upstream c.Config handoff between SetConfig and the loop's startup EvalAll.
func startSetConfigServer(t *testing.T) (pb.JoystickControlClient, *cc.Controller, func()) {
	t.Helper()

	configCtl := &cc.Controller{}

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterJoystickControlServer(srv, &GRPCServer{ConfigCtl: configCtl})
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return pb.NewJoystickControlClient(conn), configCtl, cleanup
}

// setConfigReq wraps the "config" object of the given document into the
// structpb form the RPC carries.
func setConfigReq(t *testing.T, configJson string) *pb.SetConfigReq {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal([]byte(configJson), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}
	configVal, ok := doc["config"].(map[string]any)
	if !ok {
		t.Fatalf("setup: document has no config object")
	}
	st, err := structpb.NewStruct(configVal)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return &pb.SetConfigReq{Config: st}
}

func TestSetConfigRefusesReadCycle(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	_, err := client.SetConfig(context.Background(), setConfigReq(t, `{
		"config": {
			"input_output_map": {
				"a": {"id": "a", "type": "read", "read": {"source": "b"}},
				"b": {"id": "b", "type": "read", "read": {"source": "a"}}
			}
		}
	}`))

	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for a read cycle, got %v (err: %v)",
			status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "read cycle") {
		t.Errorf("the refusal should name the problem, got: %v", err)
	}
	if configCtl.Config != nil {
		t.Errorf("a refused config must not be applied; ConfigCtl.Config is set")
	}
}

func TestSetConfigAcceptsAcyclicConfig(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	_, err := client.SetConfig(context.Background(), setConfigReq(t, `{
		"config": {
			"input_output_map": {
				"n": {"id": "n", "type": "number", "number": {"output": 0}},
				"r": {"id": "r", "type": "read", "read": {"source": "n"}}
			}
		}
	}`))

	if err != nil {
		t.Fatalf("an acyclic config must still load: %v", err)
	}
	if configCtl.Config == nil {
		t.Errorf("the accepted config should have been applied")
	}
}
