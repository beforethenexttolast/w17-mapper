// SPDX-FileCopyrightText: © 2023 ZhouYixun 291028775@qq.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
func configPayload(configJson []byte) (*structpb.Struct, error) {
	var doc map[string]any
	if err := json.Unmarshal(configJson, &doc); err != nil {
		return nil, fmt.Errorf("the saved profile is not valid JSON: %w", err)
	}

	if inner, wrapped := doc["config"].(map[string]any); wrapped {
		doc = inner
	}

	payload, err := structpb.NewStruct(doc)
	if err != nil {
		return nil, fmt.Errorf("the saved profile could not be encoded for the drive program: %w", err)
	}
	return payload, nil
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
func Init(txServerPortName, configFilePath string, txServerPortBaudRate, grpcPort int, disableWebUI bool) error {
	startLinkRequested := len(txServerPortName) != 0 && txServerPortBaudRate != 0
	if !startLinkRequested && len(configFilePath) == 0 && !disableWebUI {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, fmt.Sprintf("localhost:%d", grpcPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("could not reach the drive program's own control port %d: %w", grpcPort, err)
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
		if payload, err = configPayload(configJson); err != nil {
			return err
		}

		if res, err = client.SetConfig(ctx, &pb.SetConfigReq{Config: payload}); err != nil {
			return fmt.Errorf("the saved profile %s was refused: %w", configFilePath, err)
		}

		fmt.Printf("%v", res)
	}

	if disableWebUI {
		if res, err = client.StopHTTP(ctx, &pb.Empty{}); err != nil {
			return fmt.Errorf("could not stop the web UI: %w", err)
		}

		fmt.Printf("%v", res)
	}

	return nil
}
