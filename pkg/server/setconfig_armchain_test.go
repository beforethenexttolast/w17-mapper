// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

// The load path's teeth for the W17 arm chain (review finding MAP-12, owner
// decision OD-9/D2(a)).
//
// These drive the REAL committed profile through the real RPC, with the two
// placeholders filled the way a bench operator fills them, because the defect
// was precisely that the shape was checked against the repo's copy while race
// day loads a hand-edited one. A test that built its own config in Go would
// re-create the same blind spot.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const w17ProfilePath = "../../configs/w17-ds4.json"

// filledW17Profile is the shipped profile with its two machine-specific
// placeholders replaced, i.e. the document race day actually loads.
func filledW17Profile(t *testing.T) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(w17ProfilePath)
	if err != nil {
		t.Fatalf("the committed W17 profile must exist: %v", err)
	}

	filled := strings.ReplaceAll(string(raw), "REPLACE-WITH-COM-PORT", "COM7")
	filled = strings.ReplaceAll(filled, "REPLACE-WITH-DS4-ID", "a1b2c3")
	if strings.Contains(filled, "REPLACE-WITH-") {
		t.Fatalf("setup: the profile carries a placeholder this test does not know how to " +
			"fill, so the arm-chain refusal would never be reached")
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(filled), &doc); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return doc
}

// configOf pulls the inner config object out and wraps it the way the RPC
// carries it -- the same unwrap pkg/client.configPayload does.
func configOf(t *testing.T, doc map[string]any) *pb.SetConfigReq {
	t.Helper()

	inner, ok := doc["config"].(map[string]any)
	if !ok {
		t.Fatal("setup: document has no config object")
	}
	st, err := structpb.NewStruct(inner)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return &pb.SetConfigReq{Config: st}
}

// editSeqField reaches into the decoded document and rewrites one field of
// every seq node -- a hand-edit, expressed the way a hand-edit happens.
func editSeqField(node any, field string, value any) {
	switch v := node.(type) {
	case map[string]any:
		if kind, _ := v["type"].(string); kind == "seq" {
			if seq, ok := v["seq"].(map[string]any); ok {
				seq[field] = value
			}
		}
		for _, child := range v {
			editSeqField(child, field, value)
		}
	case []any:
		for _, child := range v {
			editSeqField(child, field, value)
		}
	}
}

// TestSetConfigAcceptsTheFilledW17Profile is the floor: the shipped profile,
// once matched to a machine, still loads. If the marker or the new lint broke
// that, every other assertion here would be worthless.
func TestSetConfigAcceptsTheFilledW17Profile(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	if _, err := client.SetConfig(context.Background(), configOf(t, filledW17Profile(t))); err != nil {
		t.Fatalf("the filled W17 profile must load: %v", err)
	}
	if configCtl.Config == nil {
		t.Fatal("the accepted profile was not applied")
	}
	if !configCtl.Config.IsW17Profile() {
		t.Error("the W17 marker did not survive the load path -- the arm-chain rules " +
			"would be silent on the file that matters")
	}
}

// TestSetConfigRefusesAW17ProfileWithResetOnNaNRemoved is MAP-12's proof. This
// edit is schema-valid (reset_on_nan defaults to false), passes the read-cycle
// check, passes the placeholder refusal and passes the endpoint lint -- and it
// re-opens review blocker F2: the arm toggle then HOLDS "armed" through a
// gamepad dropout, and the pad's auto-reconnect re-arms the car with no user
// input at all.
func TestSetConfigRefusesAW17ProfileWithResetOnNaNRemoved(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	doc := filledW17Profile(t)
	editSeqField(doc, "reset_on_nan", false)

	_, err := client.SetConfig(context.Background(), configOf(t, doc))
	if err == nil {
		t.Fatal("a W17-marked profile whose arm toggle lost reset_on_nan was ACCEPTED")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
	for _, want := range []string{"w17_profile", "reset_on_nan"} {
		if !strings.Contains(st.Message(), want) {
			t.Errorf("the refusal does not name %q: %s", want, st.Message())
		}
	}
	if configCtl.Config != nil {
		t.Error("the refused profile was applied anyway -- the refusal must run BEFORE " +
			"the config goes live")
	}
}

// TestSetConfigRefusesAW17ProfileWhoseToggleBootsArmed is the other edit that
// looks harmless in a text editor and is not.
func TestSetConfigRefusesAW17ProfileWhoseToggleBootsArmed(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	doc := filledW17Profile(t)
	editSeqField(doc, "output_values", []any{1811.0, 172.0})

	_, err := client.SetConfig(context.Background(), configOf(t, doc))
	if err == nil {
		t.Fatal("a W17-marked profile whose arm toggle boots ARMED was accepted")
	}
	if !strings.Contains(status.Convert(err).Message(), "boot disarmed") {
		t.Errorf("the refusal does not say what is wrong: %s", status.Convert(err).Message())
	}
	if configCtl.Config != nil {
		t.Error("the refused profile was applied anyway")
	}
}

// TestSetConfigStillAcceptsAnUnmarkedConfigWithABareSeq keeps the hobbyist path
// open: without the marker, nothing here applies.
func TestSetConfigStillAcceptsAnUnmarkedConfigWithABareSeq(t *testing.T) {
	client, configCtl, cleanup := startSetConfigServer(t)
	defer cleanup()

	doc := filledW17Profile(t)
	editSeqField(doc, "reset_on_nan", false)
	inner, _ := doc["config"].(map[string]any)
	delete(inner, "w17_profile")

	if _, err := client.SetConfig(context.Background(), configOf(t, doc)); err != nil {
		t.Fatalf("an UNMARKED config with a bare seq must still load -- this fork still "+
			"serves upstream rigs: %v", err)
	}
	if configCtl.Config == nil {
		t.Error("the accepted config was not applied")
	}
}
