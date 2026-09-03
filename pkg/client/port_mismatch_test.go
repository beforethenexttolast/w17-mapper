// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package client

// Tests for the explicit-port mismatch warning (review finding B1).
//
// The hazard the warning names: an explicit -tx-serial-port-name "wins", and
// what it wins is a link on a port the profile does not name. The send loop
// resolves a port's channel array by matching the link's port name against the
// map the config layer publishes -- keyed by the transmitter's own tx.port --
// and finding no entry it writes NO channel frame at all, deliberately, so the
// receiver's link-loss failsafe can fire. The operator therefore sees a running
// process, an open link and a dead car, with nothing naming the reason.
//
// configs/README.md has carried the caveat since the profile shipped; these
// pin the code half, so the sentence is where the operator is looking.

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kaack/elrs-joystick-control/pkg/server"
)

// TestExplicitPortMismatchWarningFires is the finding itself: a flag that
// disagrees with the profile's single transmitter port must produce a warning
// that names BOTH ports and says nothing is transmitted.
func TestExplicitPortMismatchWarningFires(t *testing.T) {
	doc := map[string]any{
		"input_output_map": map[string]any{
			"tx": map[string]any{"type": "tx", "tx": map[string]any{"port": benchPort}},
		},
	}

	warning := explicitPortMismatch("COM9", doc)
	if warning == "" {
		t.Fatal("an explicit port that disagrees with the profile must warn")
	}
	for _, want := range []string{"COM9", benchPort, "NOTHING IS TRANSMITTED"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning must mention %q; got: %s", want, warning)
		}
	}
}

// TestExplicitPortMismatchStaysQuiet is the over-warning guard. The three
// shapes it must NOT complain about: agreement, a profile with no transmitter,
// and a profile with several (which this cannot judge -- StartSupervisor serves
// one port at a time, so there is no single right answer to compare against).
func TestExplicitPortMismatchStaysQuiet(t *testing.T) {
	txNode := func(port string) map[string]any {
		return map[string]any{"type": "tx", "tx": map[string]any{"port": port}}
	}

	for _, tc := range []struct {
		name     string
		explicit string
		doc      map[string]any
	}{
		{
			name:     "the flag agrees with the profile",
			explicit: benchPort,
			doc: map[string]any{"input_output_map": map[string]any{
				"tx": txNode(benchPort),
			}},
		},
		{
			name:     "the profile declares no transmitter",
			explicit: "COM9",
			doc: map[string]any{"input_output_map": map[string]any{
				"n": map[string]any{"type": "number"},
			}},
		},
		{
			name:     "the profile declares several transmitters",
			explicit: "COM9",
			doc: map[string]any{"input_output_map": map[string]any{
				"tx1": txNode("COM5"),
				"tx2": txNode("COM7"),
			}},
		},
	} {
		if got := explicitPortMismatch(tc.explicit, tc.doc); got != "" {
			t.Errorf("%s: expected no warning, got: %s", tc.name, got)
		}
	}
}

// TestHeadlessBringupWarnsWhenTheExplicitPortDisagrees drives the whole
// bring-up -- the real client -> gRPC -> SetConfig path with the real committed
// profile -- and reads what the operator would read on stdout.
func TestHeadlessBringupWarnsWhenTheExplicitPortDisagrees(t *testing.T) {
	rec, _, port := startMapper(t)
	profile := filledProfile(t)

	var initErr error
	out := captureStdout(t, func() {
		initErr = Init(server.DefaultBindHost, "COM9", profile, benchBaudRate, port, false)
	})
	if initErr != nil {
		t.Fatalf("a mismatched port is a warning, not a failure: %v", initErr)
	}

	// The explicit flag still wins outright, and still starts exactly one link.
	calls := rec.links()
	if len(calls) != 1 || calls[0].GetPort() != "COM9" {
		t.Fatalf("the explicit flag must still win with one link; got %v", calls)
	}

	if !strings.Contains(out, "(bring-up) WARNING:") ||
		!strings.Contains(out, "COM9") || !strings.Contains(out, benchPort) {
		t.Errorf("the bring-up must warn that the two ports disagree; stdout was:\n%s", out)
	}
}

// TestHeadlessBringupIsQuietWhenThePortsAgree is the same path with the flag
// set to the profile's own port: exactly the case an operator following
// configs/README.md produces, and it must not be shouted at.
func TestHeadlessBringupIsQuietWhenThePortsAgree(t *testing.T) {
	_, _, port := startMapper(t)
	profile := filledProfile(t)

	var initErr error
	out := captureStdout(t, func() {
		initErr = Init(server.DefaultBindHost, benchPort, profile, benchBaudRate, port, false)
	})
	if initErr != nil {
		t.Fatalf("bring-up failed: %v", initErr)
	}
	if strings.Contains(out, "(bring-up) WARNING:") {
		t.Errorf("matching ports must not warn; stdout was:\n%s", out)
	}
}

// captureStdout runs f with os.Stdout redirected and returns what was written.
// The bring-up talks to the operator with fmt.Print, so this is the only way to
// assert on what the operator actually sees.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	saved := os.Stdout
	os.Stdout = w

	collected := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		collected <- buf.String()
	}()

	func() {
		defer func() {
			os.Stdout = saved
			_ = w.Close()
		}()
		f()
	}()

	out := <-collected
	_ = r.Close()
	return out
}

// TestSelfStartTellsAnEmptyPortFromNoTransmitter is review finding N5. The two
// states read identically to TransmitterPorts, which filters an empty tx.port
// out -- so a transmitter whose port was left blank used to be reported as "the
// saved profile declares no transmitter", sending the operator to look for a
// missing node instead of an empty field.
//
// Neither is an error: there is no link to start either way, and a bring-up
// failure is reserved for a link that was asked for and failed.
func TestSelfStartTellsAnEmptyPortFromNoTransmitter(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  map[string]any
		want string
		deny string
	}{
		{
			name: "no transmitter at all",
			doc: map[string]any{"input_output_map": map[string]any{
				"n": map[string]any{"type": "number"},
			}},
			want: "declares no transmitter",
			deny: "tx.port",
		},
		{
			name: "a transmitter whose port was left empty",
			doc: map[string]any{"input_output_map": map[string]any{
				"tx": map[string]any{"type": "tx", "tx": map[string]any{"port": ""}},
			}},
			want: "(tx.port) is empty",
			deny: "declares no transmitter",
		},
	} {
		var err error
		out := captureStdout(t, func() {
			// The client is never reached: both cases return before any RPC.
			err = selfStartLink(context.Background(), nil, tc.doc, benchBaudRate)
		})

		if err != nil {
			t.Errorf("%s: nothing to start is not a failure, got %v", tc.name, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: expected %q in the bring-up output; got:\n%s", tc.name, tc.want, out)
		}
		if strings.Contains(out, tc.deny) {
			t.Errorf("%s: the message must not say %q; got:\n%s", tc.name, tc.deny, out)
		}
	}
}
