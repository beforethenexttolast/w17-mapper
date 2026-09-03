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
	"path/filepath"
	"strconv"
	"strings"
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

// The values a bench operator fills the shipped placeholders in with. The port
// is never opened -- StartLink is recorded, not executed -- but it has to be
// the SAME string the profile carries, because that is exactly what the
// self-start has to get right.
const (
	benchPort     = "COM17"
	benchGamepad  = "a1b2c3"
	benchBaudRate = 921600
)

// filledProfile writes a copy of the committed profile with its placeholders
// filled in, the way a bench operator would, and returns its path. Everything
// else about the file is byte-for-byte the shipped artifact.
func filledProfile(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: the committed profile must exist: %v", err)
	}

	filled := strings.ReplaceAll(string(raw), "REPLACE-WITH-COM-PORT", benchPort)
	filled = strings.ReplaceAll(filled, "REPLACE-WITH-DS4-ID", benchGamepad)
	if strings.Contains(filled, cc.PlaceholderPrefix) {
		t.Fatalf("setup: the shipped profile grew a placeholder this helper does not fill in")
	}

	path := filepath.Join(t.TempDir(), "w17-ds4-filled.json")
	if err := os.WriteFile(path, []byte(filled), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return path
}

// unmarkedFilledProfile is the filled profile with its `"w17_profile": true`
// line deleted -- the exact one-token edit independent review demonstrated on
// 2026-09-04: with the marker gone, LintW17ArmChain returns nothing and a
// profile whose arm chain has been broken loads and applies.
func unmarkedFilledProfile(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filledProfile(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	const marker = `"w17_profile": true,`
	if !strings.Contains(string(raw), marker) {
		t.Fatalf("setup: the shipped profile no longer carries %s verbatim", marker)
	}
	unmarked := strings.Replace(string(raw), marker, "", 1)

	path := filepath.Join(t.TempDir(), "w17-ds4-unmarked.json")
	if err := os.WriteFile(path, []byte(unmarked), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return path
}

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

// TestW17ProfileHeadlessBringup is the MAP-1 gate: the committed profile, once
// a bench operator has filled its two machine-specific placeholders in, must be
// ACCEPTED and APPLIED through the real client -> gRPC -> SetConfig path.
// Before the unwrap fix this returned InvalidArgument and client.Init panicked,
// so the documented `-config-file-path` invocation -- and the whole race-day
// launch built on it -- could not start on any machine.
func TestW17ProfileHeadlessBringup(t *testing.T) {
	_, configCtl, port := startMapper(t)

	if err := Init(server.DefaultBindHost, "", filledProfile(t), benchBaudRate, port, false); err != nil {
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

// TestW17ProfileHeadlessBringupStartsTheLink is the MAP-2 gate (owner decision
// OD-5(a)): loading a profile must also START TRANSMITTING, on the port the
// profile itself names.
//
// Race day cannot say the port out loud -- the ground station builds mapper
// argv from a pure whitelist carrying only -config-file-path, pinned by two of
// its own tests -- so before this, one press of RACE DAY produced a process
// that had a config and sent nothing, while the card said "running".
func TestW17ProfileHeadlessBringupStartsTheLink(t *testing.T) {
	rec, _, port := startMapper(t)

	if err := Init(server.DefaultBindHost, "", filledProfile(t), benchBaudRate, port, false); err != nil {
		t.Fatalf("bring-up failed: %v", err)
	}

	calls := rec.links()
	if len(calls) != 1 {
		t.Fatalf("loading a profile must start exactly one radio link, got %d", len(calls))
	}
	if calls[0].GetPort() != benchPort {
		t.Errorf("the link must be started on the profile's own tx.port %q, got %q",
			benchPort, calls[0].GetPort())
	}
	if calls[0].GetBaudRate() != benchBaudRate {
		t.Errorf("baud rate = %d, want %d", calls[0].GetBaudRate(), benchBaudRate)
	}
}

// TestHeadlessBringupDoesNotDoubleStartTheLink keeps the hobbyist path intact:
// when -tx-serial-port-name IS given, that explicit request is the only one --
// the self-start must not add a second StartLink the supervisor would refuse
// with "link is already active".
func TestHeadlessBringupDoesNotDoubleStartTheLink(t *testing.T) {
	rec, _, port := startMapper(t)

	if err := Init(server.DefaultBindHost, "COM9", filledProfile(t), benchBaudRate, port, false); err != nil {
		t.Fatalf("bring-up failed: %v", err)
	}

	calls := rec.links()
	if len(calls) != 1 {
		t.Fatalf("an explicit -tx-serial-port-name must start exactly one link, got %d", len(calls))
	}
	if calls[0].GetPort() != "COM9" {
		t.Errorf("the explicit flag must win, got %q", calls[0].GetPort())
	}
}

// bareUpstreamConfig is a minimal, valid, UNMARKED config document -- the shape
// an upstream user of this fork would have: one number node, no transmitter,
// no W17 marker.
const bareUpstreamConfig = `{"config":{"input_output_map":{"n":{"id":"n","type":"number","number":{"output":0}}}}}`

// TestHeadlessBringupRefusesAnUnmarkedProfile is the OD-9/D2-addendum gate
// (owner ruling, 2026-09-04): `-config-file-path` is the W17 race-day path, so
// a profile that does not declare `"w17_profile": true` is refused there,
// before SetConfig -- no config applied, no radio started.
//
// It is the structural half of the fix independent review asked for. The arm
// chain is checked only for a MARKED profile, so deleting the marker from the
// car's own filled profile silenced every one of those rules on the copy that
// matters; the refusal message was reworded not to suggest it, and this makes
// the edit stop working on the path the car starts from.
//
// COST, stated plainly: this fork's binary no longer starts an upstream rig
// from the command line. The web editor still applies those configs (the test
// below), and that is where they belong.
func TestHeadlessBringupRefusesAnUnmarkedProfile(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(*testing.T) string
	}{
		{"the car's own filled profile with the marker deleted", unmarkedFilledProfile},
		{"an upstream config that never had one", func(t *testing.T) string {
			t.Helper()
			path := filepath.Join(t.TempDir(), "no-tx.json")
			if err := os.WriteFile(path, []byte(bareUpstreamConfig), 0o600); err != nil {
				t.Fatalf("setup: %v", err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, configCtl, port := startMapper(t)
			path := tc.path(t)

			err := Init(server.DefaultBindHost, "", path, benchBaudRate, port, false)
			if err == nil {
				t.Fatal("an unmarked profile must be refused by the headless bring-up")
			}
			if want := cc.W17MarkerRefusal(path); err.Error() != want {
				t.Errorf("the refusal must be the one plain sentence, verbatim.\n got: %s\nwant: %s",
					err.Error(), want)
			}
			if configCtl.Config != nil {
				t.Error("a refused profile must not become the live config -- SetConfig ran")
			}
			if calls := rec.links(); len(calls) != 0 {
				t.Errorf("a refused profile must not start the radio; got StartLink%v", calls)
			}
		})
	}
}

// TestTheEditorStaysPermissiveForUnmarkedConfigs is the other half of the same
// ruling: SetConfig -- the web editor's Apply, and the authoritative gate for
// everything else -- must still accept an unmarked upstream config. If this
// ever fails, the refusal above has leaked out of the race-day path and this
// fork has stopped being usable for the rigs it inherited.
func TestTheEditorStaysPermissiveForUnmarkedConfigs(t *testing.T) {
	_, configCtl, port := startMapper(t)

	payload, doc, err := configPayload([]byte(bareUpstreamConfig))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if cc.DeclaresW17Marker(doc) {
		t.Fatal("setup: this fixture is supposed to be unmarked")
	}

	conn, err := grpc.Dial("127.0.0.1:"+strconv.Itoa(port),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer conn.Close()

	if _, err = pb.NewJoystickControlClient(conn).SetConfig(context.Background(),
		&pb.SetConfigReq{Config: payload}); err != nil {
		t.Fatalf("SetConfig must still accept an unmarked upstream config: %v", err)
	}
	if configCtl.Config == nil {
		t.Error("the editor path reported success but applied nothing")
	}
}

// TestSelfStartLinkWithoutATransmitterStartsNothing keeps the coverage the
// refusal above took away from Init: a config with no tx node has no link to
// start, and that is not an error. It drives selfStartLink directly, against
// the same recording server, because the document it needs is one the headless
// path no longer accepts.
func TestSelfStartLinkWithoutATransmitterStartsNothing(t *testing.T) {
	rec, _, port := startMapper(t)

	_, doc, err := configPayload([]byte(bareUpstreamConfig))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	conn, err := grpc.Dial("127.0.0.1:"+strconv.Itoa(port),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer conn.Close()

	if err := selfStartLink(context.Background(),
		pb.NewJoystickControlClient(conn), doc, benchBaudRate); err != nil {
		t.Fatalf("a config with no transmitter is not an error: %v", err)
	}
	if calls := rec.links(); len(calls) != 0 {
		t.Errorf("there is no port to start; got StartLink%v", calls)
	}
}

// TestHeadlessBringupRefusesUnfilledProfile is the MAP-5 gate (owner decision
// OD-9/D3): the profile AS SHIPPED, still carrying both placeholders, must be
// refused with one plain sentence rather than loaded and silently inert.
//
// The state it refuses is fail-safe but mute: an unresolved gamepad id matches
// no device, so every channel sits on its failsafe and arm stays on the 172
// disarm rail -- the car is safe and completely dead, and nothing said why.
func TestHeadlessBringupRefusesUnfilledProfile(t *testing.T) {
	rec, configCtl, port := startMapper(t)

	err := Init(server.DefaultBindHost, "", w17ProfilePath, benchBaudRate, port, false)
	if err == nil {
		t.Fatal("the shipped profile still has placeholders in it and must be refused")
	}

	want := cc.PlaceholderRefusal([]string{"REPLACE-WITH-COM-PORT", "REPLACE-WITH-DS4-ID"})
	if err.Error() != want {
		t.Errorf("the refusal must be the one plain sentence, verbatim.\n got: %s\nwant: %s",
			err.Error(), want)
	}

	if configCtl.Config != nil {
		t.Error("a refused profile must not become the live config")
	}
	if calls := rec.links(); len(calls) != 0 {
		t.Errorf("a refused profile must not start the radio; got StartLink%v", calls)
	}
}

// TestSetConfigRefusesUnfilledProfileServerSide pins the SERVER half of the
// same rule -- the authoritative gate, which also covers the web editor's
// Apply, and which a client that skipped its own check could not bypass.
func TestSetConfigRefusesUnfilledProfileServerSide(t *testing.T) {
	_, configCtl, port := startMapper(t)

	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	payload, _, err := configPayload(raw)
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
		&pb.SetConfigReq{Config: payload})
	if err == nil {
		t.Fatal("SetConfig must refuse a profile with unfilled placeholders")
	}
	if !strings.Contains(err.Error(), "has not been matched to this computer yet") {
		t.Errorf("the server refusal should carry the same plain sentence, got: %v", err)
	}
	if configCtl.Config != nil {
		t.Error("a refused config must not be applied")
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
	payload, _, err := configPayload([]byte(`{"input_output_map":{"n":{"id":"n","type":"number","number":{"output":0}}}}`))
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

	payload, _, err := configPayload(raw)
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
