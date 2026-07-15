// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"github.com/kaack/elrs-joystick-control/pkg/headintent"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// headIntentStateToPb maps a receiver state string to the protobuf enum. Unknown
// strings map to UNSPECIFIED so a defaulted/garbled value is never read as a real
// state. There is deliberately no "active" (control) value.
var headIntentStateToPb = map[string]pb.HeadIntentState{
	headintent.StateDisabled:      pb.HeadIntentState_HEAD_INTENT_STATE_DISABLED,
	headintent.StateFault:         pb.HeadIntentState_HEAD_INTENT_STATE_FAULT,
	headintent.StateIdle:          pb.HeadIntentState_HEAD_INTENT_STATE_IDLE,
	headintent.StateInvalid:       pb.HeadIntentState_HEAD_INTENT_STATE_INVALID,
	headintent.StateStale:         pb.HeadIntentState_HEAD_INTENT_STATE_STALE,
	headintent.StateInactive:      pb.HeadIntentState_HEAD_INTENT_STATE_INACTIVE,
	headintent.StateNotCentered:   pb.HeadIntentState_HEAD_INTENT_STATE_NOT_CENTERED,
	headintent.StateActiveLogOnly: pb.HeadIntentState_HEAD_INTENT_STATE_ACTIVE_LOG_ONLY,
}

// headIntentToPb converts a read-only diagnostics snapshot to its wire form. The
// last valid sample is preserved (has_last_valid + the sample fields) even when the
// current state is invalid/stale/fault. receive_age_ms is the mapper's own
// server-computed value; the iPhone timestamp is never the freshness authority.
func headIntentToPb(d headintent.Diagnostics) *pb.HeadIntentDiagnostics {
	state, ok := headIntentStateToPb[d.State]
	if !ok {
		state = pb.HeadIntentState_HEAD_INTENT_STATE_UNSPECIFIED
	}
	out := &pb.HeadIntentDiagnostics{
		State:          state,
		TotalCount:     d.Counts.Total,
		ValidCount:     d.Counts.Valid,
		InvalidCount:   d.Counts.Invalid,
		SeqGaps:        d.SeqGaps,
		SeqRepeats:     d.SeqRepeats,
		SeqRegressions: d.SeqRegressions,
		RatePerSec:     int32(d.RatePerSec),
		StaleMs:        d.StaleMs,
		LastError:      d.LastError,
	}
	if d.PacketAgeMs != nil {
		out.ReceiveAgeMs = *d.PacketAgeMs
	}
	if d.SenderClockDeltaMs != nil {
		out.SenderClockDeltaMs = *d.SenderClockDeltaMs
	}
	if d.LastValid != nil {
		p := d.LastValid
		out.HasLastValid = true
		out.LastValidSeq = p.Seq
		out.YawDeg = p.YawDeg
		out.PitchDeg = p.PitchDeg
		out.RollDeg = p.RollDeg
		out.TrackingEnabled = p.TrackingEnabled
		if p.Centered != nil {
			out.HasCentered = true
			out.Centered = *p.Centered
		}
		if p.TimeoutMs != nil {
			out.HasSenderTimeout = true
			out.SenderTimeoutMs = int32(*p.TimeoutMs)
		}
	}
	return out
}

// WatchHeadIntentDiagnostics streams the mapper's authoritative, read-only
// head-intent diagnostics to a subscriber (Electron). It is strictly one-way:
// there is no request payload beyond Empty and no client->server method. A slow or
// disconnected subscriber cannot affect the receiver or any mapper state — the
// broadcaster keeps only the latest snapshot per subscriber and never blocks.
func (s *GRPCServer) WatchHeadIntentDiagnostics(_ *pb.Empty, stream pb.JoystickControl_WatchHeadIntentDiagnosticsServer) error {
	if s.HeadIntent == nil {
		return status.Error(codes.Unavailable,
			"head-intent ingest is disabled; start the mapper with -headtrack-ingest")
	}

	ch, unsubscribe, err := s.HeadIntent.Subscribe()
	if err != nil {
		// Capped fan-out: refuse extra subscribers instead of unbounded growth.
		return status.Error(codes.ResourceExhausted, err.Error())
	}
	defer unsubscribe()

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case d := <-ch:
			if err := stream.Send(headIntentToPb(d)); err != nil {
				return err
			}
		}
	}
}
