// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/golang/protobuf/jsonpb"
	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/headintent"
	lc "github.com/kaack/elrs-joystick-control/pkg/link"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	sc "github.com/kaack/elrs-joystick-control/pkg/serial"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"reflect"
	"time"
)

type GRPCServer struct {
	pb.UnimplementedJoystickControlServer
	DevicesCtl *dc.Controller
	SerialCtl  *sc.Controller
	ConfigCtl  *cc.Controller
	LinkCtl    *lc.Controller
	// HTTPCtl is the web-UI lifecycle, as an interface -- see HTTPController.
	// It is nil in tests that exercise only the config/link RPCs; the two HTTP
	// RPCs report Unavailable rather than panicking in that case. Read it
	// through httpController(), which also catches a TYPED nil (review finding
	// N4).
	HTTPCtl HTTPController
	// HeadIntent is the read-only, LOG-ONLY head-intent diagnostics source. It is
	// nil unless the mapper was started with -headtrack-ingest; when nil the
	// WatchHeadIntentDiagnostics RPC reports Unavailable. It is a diagnostics
	// consumer only and has no control path (see pkg/headintent/doc.go).
	HeadIntent *headintent.Broadcaster
}

func (s *GRPCServer) GetGamepads(context.Context, *pb.Empty) (*pb.GetGamepadsRes, error) {

	var res pb.GetGamepadsRes
	// W17 fork modification (MAP-6): a snapshot, not the live map. The poll
	// goroutine now inserts and deletes entries as devices come and go, so
	// ranging the field directly is a data race.
	for _, device := range s.DevicesCtl.GamepadList() {
		var (
			protoDevice pb.Gamepad
			deviceJson  []byte
			err         error
		)
		if deviceJson, err = json.Marshal(device); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		if err := protojson.Unmarshal(deviceJson, &protoDevice); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		res.Gamepads = append(res.Gamepads, &protoDevice)
	}

	return &res, nil
}

func (s *GRPCServer) GetTransmitters(context.Context, *pb.Empty) (*pb.GetTransmitterRes, error) {
	ports, err := s.SerialCtl.GetSerialPorts()
	if err != nil {
		return nil, err
	}

	var res pb.GetTransmitterRes
	for _, port := range ports {

		res.Transmitters = append(res.Transmitters, &pb.Transmitter{
			Port: port.Name,
			Name: port.Product,
		})
	}

	return &res, nil
}

func (s *GRPCServer) GetConfig(context.Context, *pb.Empty) (*pb.GetConfigRes, error) {
	var configJson []byte
	var err error

	// W17 fork modification (review finding N2): the live config is read
	// through the accessor, under the same lock SetConfig writes it with. This
	// handler runs on a gRPC worker goroutine while an apply can be in flight
	// on another, and the bare field read here was a real data race the -race
	// suite simply never drove.
	if configJson, err = json.Marshal(s.ConfigCtl.GetConfig()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	var configPb structpb.Struct
	m := jsonpb.Unmarshaler{}
	if err = m.Unmarshal(bytes.NewReader(configJson), &configPb); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	res := &pb.GetConfigRes{
		Config: &configPb,
	}

	return res, nil
}

func (s *GRPCServer) SetConfig(_ context.Context, req *pb.SetConfigReq) (*pb.Empty, error) {
	m := jsonpb.Marshaler{}
	js, err := m.MarshalToString(req)

	sch := cc.GetSchema()

	v := make(map[string]any)
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := sch.Validate(v); err != nil {
		return nil, cc.ValidationError(codes.InvalidArgument,
			"could not validate config against schema",
			err)
	}

	tmp := struct {
		Config *cc.Config `json:"config"`
	}{}

	// W17 fork modification: a config that fails to decode is rejected BEFORE
	// it is applied. Upstream called SetConfig first and returned the error
	// after, so a half-decoded config was already live.
	if err = json.Unmarshal([]byte(js), &tmp); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// W17 fork addition: a `read` cycle is a load-time error, not a process
	// crash or a silently inert channel. See Config.CheckReadCycles.
	if err = tmp.Config.CheckReadCycles(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// W17 fork addition (MAP-5, owner decision OD-9/D3): an UNFILLED PLACEHOLDER
	// is a hard refusal, not a warning. A profile still carrying
	// REPLACE-WITH-COM-PORT / REPLACE-WITH-DS4-ID is fail-safe -- nothing
	// resolves, so every channel sits on its failsafe and the car cannot arm --
	// but it was also completely SILENT: legal strings pass the schema, the
	// cycle check, the lint and the ground station's pre-launch checks alike,
	// and the operator's only symptom was a car that would not move. Refusing
	// here covers both load paths, the headless `-config-file-path` bring-up and
	// the editor's Apply, with the same sentence. This runs BEFORE the config is
	// applied, so a placeholder profile never becomes the live config -- which is
	// also what keeps the self-start in client.Init from ever calling StartLink
	// on the literal string "REPLACE-WITH-COM-PORT".
	if found := cc.UnfilledPlaceholders(v); len(found) != 0 {
		return nil, status.Error(codes.InvalidArgument, cc.PlaceholderRefusal(found))
	}

	// W17 fork addition (MAP-12, owner decision OD-9/D2(a)): a config that
	// DECLARES ITSELF the W17 race-day profile is held to the arm-chain shape,
	// and failing it is a refusal rather than a warning.
	//
	// The shape used to be pinned only by a test, against the copy of
	// configs/w17-ds4.json in the repo -- while race day loads a hand-edited
	// file from an absolute path on the giftee's PC. A copy with reset_on_nan
	// edited away is schema-valid (the field defaults to false) and passes every
	// other layer, so review blocker F2 could walk straight back in on the only
	// file that matters. This runs before the config is applied, so a profile
	// with a broken arm chain never becomes live.
	//
	// It is gated on the marker so upstream rigs are untouched: they may use
	// channel 5 for anything, and a fork that refused their configs would be a
	// fork nobody could use.
	if findings := cc.LintW17ArmChain(tmp.Config); len(findings) != 0 {
		return nil, status.Error(codes.InvalidArgument, cc.W17ArmChainRefusal(findings))
	}

	// W17 fork addition: plausibility lint, warnings only -- non-W17 rigs must
	// keep loading. The committed W17 profile is held to zero findings by its
	// own test. See config.LintConfig.
	for _, warning := range cc.LintConfig(tmp.Config) {
		fmt.Printf("(config-lint) WARNING: %s\n", warning)
	}

	s.ConfigCtl.SetConfig(tmp.Config)

	return &pb.Empty{}, nil
}

// httpController returns the web-UI lifecycle if there really is one.
// W17 fork addition (review finding N4).
//
// `s.HTTPCtl == nil` is NOT the whole question. HTTPCtl is an interface, and an
// interface holding a TYPED nil pointer -- (*http.Controller)(nil), which is
// what a caller that built its controller conditionally would hand over -- is
// itself non-nil. The plain comparison therefore passes and Start()/Stop() are
// called on a nil receiver, which panics inside a gRPC handler.
//
// Not reachable from the shipped binary: main always constructs a real
// controller before calling server.NewCtl. This guards the next caller, and
// the build-graph change that made HTTPCtl an interface in the first place is
// exactly what created the shape.
func (s *GRPCServer) httpController() (HTTPController, bool) {
	if s.HTTPCtl == nil {
		return nil, false
	}

	// reflect rather than a type switch on *http.Controller: pkg/server
	// deliberately does not import pkg/http (see HTTPController), and a type
	// switch would have to be extended for every future implementation.
	if v := reflect.ValueOf(s.HTTPCtl); v.Kind() == reflect.Ptr && v.IsNil() {
		return nil, false
	}

	return s.HTTPCtl, true
}

func (s *GRPCServer) StartHTTP(context.Context, *pb.Empty) (*pb.Empty, error) {
	httpCtl, ok := s.httpController()
	if !ok {
		return nil, status.Error(codes.Unavailable, "no web UI in this build")
	}
	if err := httpCtl.Start(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.Empty{}, nil
}

func (s *GRPCServer) StopHTTP(context.Context, *pb.Empty) (*pb.Empty, error) {
	httpCtl, ok := s.httpController()
	if !ok {
		return nil, status.Error(codes.Unavailable, "no web UI in this build")
	}
	if err := httpCtl.Stop(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.Empty{}, nil
}

func (s *GRPCServer) StartLink(_ context.Context, req *pb.StartLinkReq) (*pb.Empty, error) {

	if req.Port == "" {
		return nil, status.Error(codes.InvalidArgument, "port_name is required")
	}

	if req.BaudRate <= 0 {
		return nil, status.Error(codes.InvalidArgument, "baud_rate is required")
	}

	if err := s.LinkCtl.StartSupervisor(req.Port, req.BaudRate); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.Empty{}, nil
}

func (s *GRPCServer) StopLink(context.Context, *pb.Empty) (*pb.Empty, error) {
	if err := s.LinkCtl.StopSupervisor(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.Empty{}, nil
}

func (s *GRPCServer) GetGamepadStream(req *pb.GetGamepadStreamReq, server pb.JoystickControl_GetGamepadStreamServer) error {

	if req.Gamepad == nil {
		return status.Error(codes.InvalidArgument, "device is required")
	}

	if req.Gamepad.Id == "" {
		return status.Error(codes.InvalidArgument, "device.id is required")
	}

	//fmt.Printf("fetch response for id : %s\n", req.Device.Id)

	var device *dc.InputGamepad
	var ok bool
	var err error
	if device, ok = s.DevicesCtl.Gamepad(req.Gamepad.Id); !ok {
		return status.Error(codes.InvalidArgument, fmt.Sprintf("gamepad(id: %s) device not found", req.Gamepad.Id))
	}

	state := s.DevicesCtl.GetGamepadStates(device, nil)
	if err = s.StreamDeviceState(device, state, server); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Millisecond * 25)

	for {
		select {
		case <-ticker.C:
			s.DevicesCtl.AlertDeviceChan() //fake a device event to force evaluation
		case <-s.DevicesCtl.DeviceEventChan:
			if err = s.StreamDeviceState(device, state, server); err != nil {
				return err
			}
		}
	}

}

func (s *GRPCServer) GetTransmitterStream(req *pb.GetTransmitterStreamReq, server pb.JoystickControl_GetTransmitterStreamServer) error {

	if req.Transmitter == nil {
		return status.Error(codes.InvalidArgument, "device is required")
	}

	if req.Transmitter.Port == "" {
		return status.Error(codes.InvalidArgument, "device.port_name is required")
	}

	var err error

	ticker := time.NewTicker(25 * time.Millisecond)
	rfDeviceChannels := s.ConfigCtl.GetTransmitterChannels(req.Transmitter, nil)

	if err = s.StreamRfDeviceChannels(req.Transmitter, rfDeviceChannels, server); err != nil {
		return err
	}

	for {
		select {
		case <-ticker.C:
			s.DevicesCtl.AlertDeviceChan() //fake a device event to force evaluation
		case <-s.ConfigCtl.EvalEventChan:
			if err = s.StreamRfDeviceChannels(req.Transmitter, rfDeviceChannels, server); err != nil {
				return err
			}
		}
	}

}

func (s *GRPCServer) GetEvalStream(_ *pb.Empty, server pb.JoystickControl_GetEvalStreamServer) error {

	var err error

	ticker := time.NewTicker(25 * time.Millisecond)
	states := s.ConfigCtl.GetEvalStates(nil)

	if err = s.StreamEvalStates(states, server); err != nil {
		return err
	}

	for {
		select {
		case <-ticker.C:
			s.ConfigCtl.AlertStreamChan() //fake event to force config eval
		case <-s.ConfigCtl.EvalEventChan:
			if err = s.StreamEvalStates(states, server); err != nil {
				return err
			}
		}
	}
}

func (s *GRPCServer) GetLinkStream(_ *pb.Empty, server pb.JoystickControl_GetLinkStreamServer) error {

	var err error

	ticker := time.NewTicker(500 * time.Millisecond)
	state := s.LinkCtl.GetLinkState(nil)

	if err = s.StreamLinkState(state, server); err != nil {
		return err
	}

	for {
		select {
		case <-ticker.C:
			if err = s.StreamLinkState(state, server); err != nil {
				return err
			}
		}
	}

}

func (s *GRPCServer) GetTelemetryStream(_ *pb.Empty, server pb.JoystickControl_GetTelemetryStreamServer) error {

	var err error
	var telemetry *pb.Telemetry

	telemetryChan := s.LinkCtl.TelemetryBroadcaster.Subscribe()
	defer s.LinkCtl.TelemetryBroadcaster.Unsubscribe(telemetryChan)

	for {
		telemetry = <-telemetryChan
		if err = server.Send(telemetry); err != nil {
			return err
		}
	}

}

func (s *GRPCServer) GetAppInfo(_ context.Context, _ *pb.Empty) (*pb.GetAppInfoRes, error) {

	var info *VersionInfo
	var err error

	if info, err = GetVersionInfo(); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("could not unmarshal version file. %s", err.Error()))
	}

	return &pb.GetAppInfoRes{
		ReleaseTag: info.ReleaseTag,
		CommitHash: info.CommitHash,
		BranchName: info.BranchName,
	}, nil
}

func (s *GRPCServer) GetCRSFDevices(_ context.Context, _ *pb.Empty) (*pb.GetCRSFDevicesRes, error) {

	luaChan := s.LinkCtl.DeviceInfoBroadcaster.Subscribe()
	defer s.LinkCtl.DeviceInfoBroadcaster.Unsubscribe(luaChan)

	var err error
	var devicesList []*pb.CRSFDeviceInfoData
	if devicesList, err = s.LinkCtl.GetCRSFDevices(); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("could not get devices. %s", err.Error()))
	}

	res := pb.GetCRSFDevicesRes{
		Devices: devicesList,
	}

	return &res, nil
}

func (s *GRPCServer) GetCRSFDeviceFields(_ context.Context, req *pb.GetCRSFDeviceFieldsReq) (*pb.GetCRSFDeviceFieldsRes, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("request payload required"))
	}

	deviceInfo := req.GetDevice()

	if deviceInfo == nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("device info required"))
	}

	var err error
	var deviceFields []*pb.CRSFDeviceFieldData

	if deviceFields, err = s.LinkCtl.GetCRSFDeviceFields(deviceInfo); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("could not get device fields. %s", err.Error()))
	}

	for _, field := range deviceFields {
		FixOptionsArrows(field)
	}

	res := pb.GetCRSFDeviceFieldsRes{
		Fields: deviceFields,
	}

	return &res, nil
}

func (s *GRPCServer) GetCRSFDeviceField(_ context.Context, req *pb.GetCRSFDeviceFieldReq) (*pb.GetCRSFDeviceFieldRes, error) {

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("request payload required"))
	}

	deviceInfo := req.GetDevice()

	if deviceInfo == nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("device is required"))
	}

	var err error
	var deviceField *pb.CRSFDeviceFieldData

	if deviceField, err = s.LinkCtl.GetCRSFDeviceField(deviceInfo, req.GetFieldId(), time.Second); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("could get device field. %s", err.Error()))
	}

	if deviceField == nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("could not get device field. %s", err.Error()))
	}

	FixOptionsArrows(deviceField)

	res := pb.GetCRSFDeviceFieldRes{
		Field: deviceField,
	}

	return &res, nil
}

func (s *GRPCServer) SetCRSFDeviceField(_ context.Context, req *pb.SetCRSFDeviceFieldReq) (*pb.SetCRSFDeviceFieldRes, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("request payload required"))
	}

	var err error
	var deviceField *pb.CRSFDeviceFieldData

	if deviceField, err = s.LinkCtl.SetCRSFDeviceField(req.GetDevice(), req.GetField()); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("could not set device field. %s", err.Error()))
	}

	res := pb.SetCRSFDeviceFieldRes{
		Field: deviceField,
	}

	return &res, nil
}

func (s *GRPCServer) GetCRSFDeviceLinkStatus(_ context.Context, _ *pb.Empty) (*pb.GetCRSFDeviceLinkStatusRes, error) {

	var err error
	var deviceLinkStatus *pb.CRSFDeviceLinkStatusData

	if deviceLinkStatus, err = s.LinkCtl.GetCRSFDeviceLinkStatus(5 * time.Second); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("could not get device link status. %s", err.Error()))
	}

	res := pb.GetCRSFDeviceLinkStatusRes{
		LinkStatus: deviceLinkStatus,
	}

	return &res, nil
}

func (s *GRPCServer) ClearCRSFDeviceLinkCriticalFlags(_ context.Context, _ *pb.Empty) (*pb.Empty, error) {

	var err error

	if err = s.LinkCtl.ClearCRSFDeviceLinkCriticalFlags(); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("could not clear device link critical flags. %s", err.Error()))
	}

	return &pb.Empty{}, nil
}
