// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kaack/elrs-joystick-control/pkg/headintent"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func fixedSource(d headintent.Diagnostics) headintent.SnapshotFunc {
	return func() headintent.Diagnostics { return d }
}

// startTestServer serves a GRPCServer with the given (possibly nil) head-intent
// broadcaster over an in-memory bufconn, returning a client and a cleanup func.
func startTestServer(t *testing.T, hi *headintent.Broadcaster) (pb.JoystickControlClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterJoystickControlServer(srv, &GRPCServer{HeadIntent: hi})
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
	return pb.NewJoystickControlClient(conn), cleanup
}

func TestWatchHeadIntentDisabledIsUnavailable(t *testing.T) {
	client, cleanup := startTestServer(t, nil) // head-intent ingest disabled
	defer cleanup()

	stream, err := client.WatchHeadIntentDiagnostics(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Recv err code = %v, want Unavailable", status.Code(err))
	}
}

func TestWatchHeadIntentInitialSnapshot(t *testing.T) {
	bc := headintent.NewBroadcaster(
		fixedSource(headintent.Diagnostics{State: headintent.StateActiveLogOnly, StaleMs: 300}),
		headintent.BroadcasterOptions{},
	)
	client, cleanup := startTestServer(t, bc)
	defer cleanup()

	stream, err := client.WatchHeadIntentDiagnostics(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if msg.State != pb.HeadIntentState_HEAD_INTENT_STATE_ACTIVE_LOG_ONLY {
		t.Errorf("initial snapshot state = %v, want ACTIVE_LOG_ONLY", msg.State)
	}
}

func TestWatchHeadIntentCapsAtFour(t *testing.T) {
	bc := headintent.NewBroadcaster(
		fixedSource(headintent.Diagnostics{State: headintent.StateIdle, StaleMs: 300}),
		headintent.BroadcasterOptions{},
	)
	client, cleanup := startTestServer(t, bc)
	defer cleanup()

	// Open 4 streams and confirm each subscribed (Recv returns the initial snapshot).
	var cancels []context.CancelFunc
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		stream, err := client.WatchHeadIntentDiagnostics(ctx, &pb.Empty{})
		if err != nil {
			t.Fatalf("open stream %d: %v", i+1, err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("stream %d first Recv: %v", i+1, err)
		}
	}

	// The 5th concurrent stream must be refused with ResourceExhausted.
	stream, err := client.WatchHeadIntentDiagnostics(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatalf("open 5th stream: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("5th stream Recv code = %v, want ResourceExhausted", status.Code(err))
	}
}

func TestWatchHeadIntentClientCancelReleasesSubscription(t *testing.T) {
	bc := headintent.NewBroadcaster(
		fixedSource(headintent.Diagnostics{State: headintent.StateIdle, StaleMs: 300}),
		headintent.BroadcasterOptions{},
	)
	client, cleanup := startTestServer(t, bc)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.WatchHeadIntentDiagnostics(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if bc.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d, want 1", bc.SubscriberCount())
	}

	cancel() // client disconnects

	deadline := time.Now().Add(3 * time.Second)
	for bc.SubscriberCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscription not released after cancel; count = %d", bc.SubscriberCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWatchHeadIntentIsServerStreamOnly proves the RPC is read-only: the service
// descriptor marks it server-streaming with NO client-streaming, so there is no
// client->server message path on this method.
func TestWatchHeadIntentIsServerStreamOnly(t *testing.T) {
	var found bool
	for _, s := range pb.JoystickControl_ServiceDesc.Streams {
		if s.StreamName == "WatchHeadIntentDiagnostics" {
			found = true
			if s.ClientStreams {
				t.Error("WatchHeadIntentDiagnostics must not be client-streaming (read-only)")
			}
			if !s.ServerStreams {
				t.Error("WatchHeadIntentDiagnostics must be server-streaming")
			}
		}
	}
	if !found {
		t.Fatal("WatchHeadIntentDiagnostics stream not found in service descriptor")
	}
}

// TestHeadIntentToPbPreservesLastValid checks the snapshot->wire conversion: state
// enum mapping, mapper-computed receive age, preserved last-valid sample fields,
// and fault text — for a STALE snapshot that still carries its last valid packet.
func TestHeadIntentToPbPreservesLastValid(t *testing.T) {
	age := int64(450)
	delta := int64(-20)
	centered := true
	timeout := 250
	d := headintent.Diagnostics{
		State:              headintent.StateStale,
		Counts:             headintent.Counts{Total: 10, Valid: 8, Invalid: 2},
		SeqGaps:            1,
		SeqRepeats:         0,
		SeqRegressions:     0,
		PacketAgeMs:        &age,
		SenderClockDeltaMs: &delta,
		RatePerSec:         0,
		StaleMs:            300,
		LastError:          "",
		LastValid: &headintent.Packet{
			Seq: 42, YawDeg: -12.5, PitchDeg: 6.8, RollDeg: 1.2,
			TrackingEnabled: true, Centered: &centered, TimeoutMs: &timeout,
		},
	}

	pbd := headIntentToPb(d)
	if pbd.State != pb.HeadIntentState_HEAD_INTENT_STATE_STALE {
		t.Errorf("state = %v, want STALE", pbd.State)
	}
	if !pbd.HasLastValid || pbd.LastValidSeq != 42 {
		t.Errorf("last valid not preserved: has=%v seq=%d", pbd.HasLastValid, pbd.LastValidSeq)
	}
	if pbd.ReceiveAgeMs != 450 {
		t.Errorf("receive_age_ms = %d, want 450 (mapper-computed)", pbd.ReceiveAgeMs)
	}
	if pbd.YawDeg != -12.5 || pbd.PitchDeg != 6.8 || pbd.RollDeg != 1.2 {
		t.Errorf("angles not preserved: %v/%v/%v", pbd.YawDeg, pbd.PitchDeg, pbd.RollDeg)
	}
	if !pbd.TrackingEnabled || !pbd.HasCentered || !pbd.Centered {
		t.Errorf("tracking/centered not preserved: %+v", pbd)
	}
	if !pbd.HasSenderTimeout || pbd.SenderTimeoutMs != 250 {
		t.Errorf("sender timeout hint not preserved: has=%v v=%d", pbd.HasSenderTimeout, pbd.SenderTimeoutMs)
	}
	if pbd.ValidCount != 8 || pbd.InvalidCount != 2 || pbd.TotalCount != 10 {
		t.Errorf("counts wrong: %+v", pbd)
	}
}

// TestHeadIntentToPbUnknownStateIsUnspecified guards the enum default.
func TestHeadIntentToPbUnknownStateIsUnspecified(t *testing.T) {
	pbd := headIntentToPb(headintent.Diagnostics{State: "bogus"})
	if pbd.State != pb.HeadIntentState_HEAD_INTENT_STATE_UNSPECIFIED {
		t.Errorf("unknown state mapped to %v, want UNSPECIFIED", pbd.State)
	}
}
