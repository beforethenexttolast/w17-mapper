// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

// The two HTTP RPCs' nil guard (review finding N4).
//
// HTTPCtl became an INTERFACE when pkg/server stopped depending on the web
// bundle, and that is what created this shape: an interface holding a typed nil
// pointer is itself non-nil, so `s.HTTPCtl == nil` is false for
// (*http.Controller)(nil) and Start()/Stop() would be called on a nil receiver
// -- a panic inside a gRPC handler.
//
// Not reachable from the shipped binary (main always constructs a real
// controller). These pin the guard for the next caller.

import (
	"context"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recordingHTTPCtl dereferences its receiver on every call, so a nil one panics
// -- which is the whole point: without the guard these tests crash the run.
type recordingHTTPCtl struct {
	starts int
	stops  int
}

func (c *recordingHTTPCtl) Start() error {
	c.starts++
	return nil
}

func (c *recordingHTTPCtl) Stop() error {
	c.stops++
	return nil
}

func TestHTTPRPCsRefuseATypedNilController(t *testing.T) {
	var typedNil *recordingHTTPCtl
	srv := &GRPCServer{HTTPCtl: typedNil}

	if srv.HTTPCtl == nil {
		t.Fatal("setup: this test is only meaningful while the interface is non-nil")
	}

	if _, err := srv.StartHTTP(context.Background(), &pb.Empty{}); status.Code(err) != codes.Unavailable {
		t.Errorf("StartHTTP on a typed-nil controller = %v, want Unavailable", err)
	}
	if _, err := srv.StopHTTP(context.Background(), &pb.Empty{}); status.Code(err) != codes.Unavailable {
		t.Errorf("StopHTTP on a typed-nil controller = %v, want Unavailable", err)
	}
}

func TestHTTPRPCsRefuseAnAbsentController(t *testing.T) {
	srv := &GRPCServer{}

	if _, err := srv.StartHTTP(context.Background(), &pb.Empty{}); status.Code(err) != codes.Unavailable {
		t.Errorf("StartHTTP with no controller = %v, want Unavailable", err)
	}
	if _, err := srv.StopHTTP(context.Background(), &pb.Empty{}); status.Code(err) != codes.Unavailable {
		t.Errorf("StopHTTP with no controller = %v, want Unavailable", err)
	}
}

// TestHTTPRPCsStillDriveARealController is the over-correction guard: the
// guard must not start refusing the controller main actually supplies.
func TestHTTPRPCsStillDriveARealController(t *testing.T) {
	ctl := &recordingHTTPCtl{}
	srv := &GRPCServer{HTTPCtl: ctl}

	if _, err := srv.StartHTTP(context.Background(), &pb.Empty{}); err != nil {
		t.Fatalf("StartHTTP: %v", err)
	}
	if _, err := srv.StopHTTP(context.Background(), &pb.Empty{}); err != nil {
		t.Fatalf("StopHTTP: %v", err)
	}
	if ctl.starts != 1 || ctl.stops != 1 {
		t.Errorf("the real controller was driven %d/%d times, want 1/1", ctl.starts, ctl.stops)
	}
}
