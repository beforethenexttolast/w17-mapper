// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package client

// End-to-end tests for the HEADLESS BRING-UP -- the only path the W17 gift
// actually uses.
//
// Race day launches the mapper with one flag, `-config-file-path <profile>`,
// and never opens the node-graph editor. Everything that happens after that is
// this package plus pkg/server: client.Init dials the mapper's own gRPC port,
// applies the saved profile, and starts the RF link. Until these tests existed
// that whole path had no coverage: pkg/config/w17_profile_test.go decodes
// configs/w17-ds4.json with json.Unmarshal directly, which is why MAP-1 -- a
// double-wrapped SetConfig payload that made the documented invocation panic
// on every machine -- survived a profile test suite that passes.
//
// So these drive the REAL path: a real pb.JoystickControlServer over a real
// loopback gRPC connection, the real SetConfig with its schema validation,
// read-cycle check and lint, and the real committed profile from configs/.
//
// What is deliberately NOT real: the serial port. StartLink is recorded rather
// than executed (see recordingServer), because opening a serial port is
// forbidden in an unattended session and because what these tests need to
// prove is WHICH port and baud the bring-up asks for -- the wiring from that
// RPC into link.StartSupervisor is one line, pkg/server/server_grpc.go, and is
// exercised by pkg/link's own tests.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"

	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"github.com/kaack/elrs-joystick-control/pkg/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// w17ProfilePath is the committed profile the ground station ships and loads.
const w17ProfilePath = "../../configs/w17-ds4.json"

// recordingServer is the real gRPC server with StartLink stubbed out: it
// records the request and reports success instead of touching a serial port.
type recordingServer struct {
	*server.GRPCServer

	mu             sync.Mutex
	startLinkCalls []*pb.StartLinkReq
}

func (r *recordingServer) StartLink(_ context.Context, req *pb.StartLinkReq) (*pb.Empty, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startLinkCalls = append(r.startLinkCalls, req)
	return &pb.Empty{}, nil
}

func (r *recordingServer) links() []*pb.StartLinkReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*pb.StartLinkReq, len(r.startLinkCalls))
	copy(out, r.startLinkCalls)
	return out
}

// startMapper serves the real gRPC surface on a free loopback port and returns
// that port, so a test can call client.Init exactly as main does.
//
// The config controller is bare -- no eval loop -- for the same reason
// pkg/server's setconfig_cycle_test.go uses a bare one: these tests are about
// the LOAD path, and a live loop would race the upstream Config handoff.
func startMapper(t *testing.T) (rec *recordingServer, configCtl *cc.Controller, port int) {
	t.Helper()

	configCtl = &cc.Controller{}
	rec = &recordingServer{GRPCServer: &server.GRPCServer{ConfigCtl: configCtl}}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup: could not listen on loopback: %v", err)
	}

	srv := grpc.NewServer()
	pb.RegisterJoystickControlServer(srv, rec)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	return rec, configCtl, lis.Addr().(*net.TCPAddr).Port
}

// TestW17ProfileHeadlessBringup is the MAP-1 gate: the committed profile, sent
// through the real client -> gRPC -> SetConfig path, must be ACCEPTED and
// APPLIED. Before the unwrap fix this returned InvalidArgument and client.Init
// panicked, so the documented `-config-file-path` invocation -- and the whole
// race-day launch built on it -- could not start on any machine.
func TestW17ProfileHeadlessBringup(t *testing.T) {
	_, configCtl, port := startMapper(t)

	if err := Init("", w17ProfilePath, 921600, port, false); err != nil {
		t.Fatalf("the committed profile must load through the headless path: %v", err)
	}

	if configCtl.Config == nil {
		t.Fatal("SetConfig reported success but no config was applied")
	}
	if len(configCtl.Config.IOMap) == 0 {
		t.Error("the applied config has an empty input_output_map")
	}
	if _, ok := configCtl.Config.IOMap["w17-tx"]; !ok {
		t.Errorf("the applied config has no w17-tx transmitter node; got keys %v",
			keysOf(configCtl.Config.IOMap))
	}
}

// TestHeadlessBringupRefusesDoubleWrappedPayload pins the defect itself, so a
// future edit cannot quietly put the wrapper back. It sends what upstream sent
// -- the WHOLE document, wrapper included -- and requires the server to refuse
// it. If this ever starts passing, the unwrap in configPayload has become
// untested rather than unnecessary.
func TestHeadlessBringupRefusesDoubleWrappedPayload(t *testing.T) {
	_, configCtl, port := startMapper(t)

	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}
	whole, err := structpb.NewStruct(doc) // the un-unwrapped document
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	conn, err := grpc.Dial("127.0.0.1:"+strconv.Itoa(port),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer conn.Close()

	_, err = pb.NewJoystickControlClient(conn).SetConfig(context.Background(),
		&pb.SetConfigReq{Config: whole})
	if err == nil {
		t.Fatal("a double-wrapped payload must be refused: the schema requires " +
			"input_output_map directly under config")
	}
	if configCtl.Config != nil {
		t.Error("a refused config must not be applied")
	}
}

// TestConfigPayloadPassesBareDocumentThrough guards the other half of the
// unwrap: the RPC also accepts a bare {"input_output_map": ...} document, and
// the fix must not start hunting for a "config" key that legitimately is not
// there.
func TestConfigPayloadPassesBareDocumentThrough(t *testing.T) {
	payload, err := configPayload([]byte(`{"input_output_map":{"n":{"id":"n","type":"number","number":{"output":0}}}}`))
	if err != nil {
		t.Fatalf("a bare config document must encode: %v", err)
	}
	if _, ok := payload.GetFields()["input_output_map"]; !ok {
		t.Errorf("the bare document lost its input_output_map: %v", payload.AsMap())
	}
	if _, wrapped := payload.GetFields()["config"]; wrapped {
		t.Error("a bare document must not acquire a config wrapper")
	}
}

// TestConfigPayloadUnwrapsSavedProfile is the unit-level statement of the same
// rule against the real file: what goes on the wire is the config OBJECT.
func TestConfigPayloadUnwrapsSavedProfile(t *testing.T) {
	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	payload, err := configPayload(raw)
	if err != nil {
		t.Fatalf("the committed profile must encode: %v", err)
	}
	if _, wrapped := payload.GetFields()["config"]; wrapped {
		t.Error("the payload still carries the file's own config wrapper -- " +
			"the server re-wraps it, so this is MAP-1 all over again")
	}
	if _, ok := payload.GetFields()["input_output_map"]; !ok {
		t.Error("the payload should be the config object itself")
	}
}

func keysOf(m map[string]*cc.IOHolder) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
