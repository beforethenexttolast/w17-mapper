// SPDX-FileCopyrightText: © 2023 ZhouYixun 291028775@qq.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// initTimeout bounds the whole headless bring-up: dial, SetConfig, StartLink,
// StopHTTP. Unchanged from upstream's inline 10 s.
const initTimeout = 10 * time.Second

// configPayload turns the bytes of a saved profile into the object
// SetConfigReq.Config must carry. W17 fork modification (MAP-1).
//
// SetConfigReq.Config is the config OBJECT, not the document: the server
// re-marshals the whole request, which puts the field back under a "config"
// key (pkg/server/server_grpc.go SetConfig -> jsonpb.MarshalToString(req)),
// and only then validates against pkg/config/schema.yaml, whose
// #/definitions/config requires input_output_map.
//
// Upstream passed the decoded FILE straight through, so a saved profile -- the
// shape the editor's own export produces, and the shape configs/w17-ds4.json
// ships in -- arrived at the server double-wrapped as
// {"config": {"config": {...}}}. input_output_map was then two levels down,
// validation failed, the RPC returned InvalidArgument, and the client panicked
// before the process could do anything at all. That is the whole of MAP-1: the
// documented `-config-file-path` invocation, and therefore the entire race-day
// launch, could never have worked on any machine.
//
// So: unwrap before the server re-wraps. A document with a top-level "config"
// object contributes that object; a bare {"input_output_map": ...} document --
// which the RPC also accepts, and which some hand-written configs use -- is
// passed through unchanged.
func configPayload(configJson []byte) (*structpb.Struct, map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(configJson, &doc); err != nil {
		return nil, nil, fmt.Errorf("the saved profile is not valid JSON: %w", err)
	}

	if inner, wrapped := doc["config"].(map[string]any); wrapped {
		doc = inner
	}

	payload, err := structpb.NewStruct(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("the saved profile could not be encoded for the drive program: %w", err)
	}
	return payload, doc, nil
}

// Init performs the headless bring-up against the mapper's own gRPC server:
// start the RF link, apply a saved profile, and/or shut the web UI down,
// depending on which flags were given.
//
// W17 fork modification: it RETURNS an error instead of panicking. Every one
// of these failures is a bring-up failure the operator has to read and act on
// -- a missing file, an unfilled profile, a COM port that is not there -- and
// a Go panic answers with a goroutine dump instead of a sentence. main prints
// the message and exits non-zero after its normal shutdown.
func Init(grpcHost, txServerPortName, configFilePath string, txServerPortBaudRate, grpcPort int, disableWebUI bool) error {
	startLinkRequested := len(txServerPortName) != 0 && txServerPortBaudRate != 0
	if !startLinkRequested && len(configFilePath) == 0 && !disableWebUI {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	// W17 fork modification (MAP-8): dial the host this process actually bound,
	// rather than the name "localhost". With the loopback default those are the
	// same machine but not always the same ADDRESS -- "localhost" resolves to
	// ::1 first on many systems, while the server binds 127.0.0.1 -- and with
	// -bind-host they need not agree at all.
	target := net.JoinHostPort(grpcHost, strconv.Itoa(grpcPort))
	conn, err := grpc.DialContext(ctx, target,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("could not reach the drive program's own control port at %s: %w", target, err)
	}
	defer conn.Close()

	client := pb.NewJoystickControlClient(conn)
	var res *pb.Empty

	if startLinkRequested {
		if res, err = client.StartLink(ctx, &pb.StartLinkReq{
			Port:     txServerPortName,
			BaudRate: int32(txServerPortBaudRate),
		}); err != nil {
			return fmt.Errorf("could not start the radio link on %s: %w", txServerPortName, err)
		}

		fmt.Printf("%v", res)
	}

	if len(configFilePath) != 0 {
		var configJson []byte
		if configJson, err = os.ReadFile(configFilePath); err != nil {
			return fmt.Errorf("could not read the saved profile %s: %w", configFilePath, err)
		}

		var payload *structpb.Struct
		var doc map[string]any
		if payload, doc, err = configPayload(configJson); err != nil {
			return err
		}

		// W17 fork addition (MAP-5, OD-9/D3): refuse an unfilled profile HERE,
		// before the RPC, so the operator reads the plain sentence on its own
		// rather than wrapped in a gRPC status. The server refuses it too (that
		// is the authoritative gate, and it also covers the editor); this is the
		// same verdict delivered readably, and it is what guarantees the
		// self-start below never sees the literal "REPLACE-WITH-COM-PORT".
		if found := cc.UnfilledPlaceholders(doc); len(found) != 0 {
			return errors.New(cc.PlaceholderRefusal(found))
		}

		if res, err = client.SetConfig(ctx, &pb.SetConfigReq{Config: payload}); err != nil {
			return fmt.Errorf("the saved profile %s was refused: %w", configFilePath, err)
		}

		fmt.Printf("%v", res)

		// W17 fork addition (MAP-2, owner decision OD-5(a)): start the RF link
		// from the profile the operator just loaded.
		//
		// Nothing did this before, so launching the mapper -- by hand or from
		// race day -- applied a config and then sat there transmitting nothing.
		// StartLink ran only when -tx-serial-port-name was passed, and race day
		// cannot pass it: the ground station builds mapper argv from a pure
		// whitelist carrying exactly one flag, -config-file-path, pinned by two
		// of its own tests. The only remaining way to start transmitting was the
		// mapper's own web UI -- the hobbyist step this fork's whole product
		// promise removes. So the "running" card meant "the process started",
		// never "frames are on the wire".
		//
		// The port comes from the profile's own tx.port because that is the only
		// port that can work: the send loop resolves channels by matching the
		// transmitter's port name, so a link on any other port transmits nothing
		// anyway. The baud rate comes from -tx-serial-port-baud-rate (default
		// 921600) because the config schema has no baud field.
		//
		// This runs strictly AFTER the placeholder refusal above, which is what
		// keeps it from ever opening "REPLACE-WITH-COM-PORT", and after SetConfig
		// has returned, which since the adoption fix means the config is live
		// before the first frame can go out.
		if !startLinkRequested {
			if err = selfStartLink(ctx, client, doc, txServerPortBaudRate); err != nil {
				return err
			}
		}
	}

	if disableWebUI {
		if res, err = client.StopHTTP(ctx, &pb.Empty{}); err != nil {
			return fmt.Errorf("could not stop the web UI: %w", err)
		}

		fmt.Printf("%v", res)
	}

	return nil
}

// selfStartLink starts the RF link on the port the just-loaded profile names.
// W17 fork addition (MAP-2 / OD-5(a)); see the call site for why.
//
// It is deliberately quiet and non-fatal about the shapes it cannot decide:
// a config with no transmitter has no link to start, and a config with several
// is a multi-radio rig this fork does not ship -- link.StartSupervisor serves
// exactly one port at a time, so guessing which one would be worse than saying
// so. Both print a line and carry on; only a link that was asked for and
// FAILED is an error, because that is the case where the operator thinks the
// car is live and it is not.
func selfStartLink(ctx context.Context, client pb.JoystickControlClient, doc map[string]any, baudRate int) error {
	ports := cc.TransmitterPorts(doc)

	switch {
	case len(ports) == 0:
		fmt.Println("(bring-up) the saved profile declares no transmitter, so there is no radio link to start")
		return nil
	case len(ports) > 1:
		fmt.Printf("(bring-up) the saved profile declares %d transmitters (%v); "+
			"this build starts one radio link at a time, so none was started automatically\n",
			len(ports), ports)
		return nil
	}

	if baudRate <= 0 {
		return fmt.Errorf("the radio link on %s could not be started: the baud rate is %d "+
			"(pass -tx-serial-port-baud-rate, or leave it at its 921600 default)", ports[0], baudRate)
	}

	fmt.Printf("(bring-up) starting the radio link on %s at %d baud, from the saved profile\n",
		ports[0], baudRate)

	res, err := client.StartLink(ctx, &pb.StartLinkReq{
		Port:     ports[0],
		BaudRate: int32(baudRate),
	})
	if err != nil {
		return fmt.Errorf("the radio link on %s could not be started: %w", ports[0], err)
	}

	fmt.Printf("%v", res)
	return nil
}
