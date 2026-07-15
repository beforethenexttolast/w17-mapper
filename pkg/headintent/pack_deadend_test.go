// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

// This test proves the safety boundary that authorizes wiring the LOG-ONLY
// head-intent receiver into cmd/elrs-joystick-control behind a disabled-by-default
// flag: the CRSF byte stream the mapper transmits (crsf.PackChannels over the
// gamepad-derived [16]CRSFValue) is byte-for-byte identical whether the receiver
// is OFF (flag off; no receiver constructed) or ON and actively processing valid,
// stale, and invalid UDP traffic. The receiver shares no state with the pack path;
// this is the empirical, regression-guarding demonstration of that dead end.
//
// It imports pkg/crossfire ONLY from this _test file, so the receiver's production
// dependency set stays clean (go list -deps ./pkg/headintent reaches no crossfire).

import (
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	crsf "github.com/kaack/elrs-joystick-control/pkg/crossfire"
	"github.com/kaack/elrs-joystick-control/pkg/util"
)

// gamepadToCRSF maps a normalized stick fraction f in [-1,1] to a CRSF channel
// count the way the mapper's axis path does: a physical gamepad axis
// (int16, util.MinRaw..util.MaxRaw) linearly mapped into the CRSF stick range
// 172..1811 with ~992 at center. This is the same util.MapRange the node graph
// uses (pkg/config/input_axis.go:104-106, pkg/util/util.go), so the resulting
// [16]CRSFValue is a faithful stand-in for real gamepad->CRSF output.
func gamepadToCRSF(f float64) util.CRSFValue {
	if f > 1 {
		f = 1
	} else if f < -1 {
		f = -1
	}
	raw := util.RawValue(f * float64(util.MaxRaw))
	mapped := util.MapRange(raw, util.MinRaw, util.MaxRaw, 172, 1811)
	return util.CRSFValue(mapped)
}

func fillCRSF(v util.CRSFValue) *[16]util.CRSFValue {
	var a [16]util.CRSFValue
	for i := range a {
		a[i] = v
	}
	return &a
}

// gamepadSnapshots is a deterministic sequence of gamepad->CRSF channel frames:
// a stick sweep spread across all 16 channels (so every channel slot and every
// 11-bit boundary in the packed frame is exercised) plus the explicit edges
// all-min (172), all-center (992), and all-max (1811).
func gamepadSnapshots() []*[16]util.CRSFValue {
	fracs := []float64{-1, -0.75, -0.5, -0.25, 0, 0.25, 0.5, 0.75, 1}
	var snaps []*[16]util.CRSFValue
	for _, f := range fracs {
		var arr [16]util.CRSFValue
		for ch := 0; ch < 16; ch++ {
			cf := f + float64(ch)*0.05
			if cf > 1 {
				cf -= 2 // wrap so channels stay distinct across the valid span
			}
			arr[ch] = gamepadToCRSF(cf)
		}
		snaps = append(snaps, &arr)
	}
	snaps = append(snaps, fillCRSF(172), fillCRSF(992), fillCRSF(1811))
	return snaps
}

// packSnapshots concatenates crsf.PackChannels over every snapshot — the exact
// bytes the send loop would write to the serial port for this gamepad sequence.
func packSnapshots(snaps []*[16]util.CRSFValue) []byte {
	var buf bytes.Buffer
	for _, s := range snaps {
		buf.Write(crsf.PackChannels(s))
	}
	return buf.Bytes()
}

// waitState polls until the running receiver reports want, or fails.
func waitState(t *testing.T, r *Receiver, want, mode string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := r.State(time.Now()); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] receiver did not reach %q; state=%q diag=%+v",
				mode, want, r.State(time.Now()), r.Diagnostics())
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// packWithReceiver starts a REAL log-only receiver on a loopback UDP socket,
// drives it with the requested traffic, waits until it has demonstrably reached
// the corresponding observable state, and only then packs the identical
// gamepad->CRSF snapshots while the receiver is live. It fails if the receiver
// did not actually process the traffic, so the "ON" arm can never be vacuous.
func packWithReceiver(t *testing.T, snaps []*[16]util.CRSFValue, mode string) []byte {
	t.Helper()

	r := NewReceiver(Options{Port: 0, BindHost: "127.0.0.1"}) // real socket, real clock
	if err := r.Start(); err != nil {
		t.Fatalf("[%s] Start: %v", mode, err)
	}
	defer r.Stop()

	client, err := net.Dial("udp4", r.LocalAddr().String())
	if err != nil {
		t.Fatalf("[%s] dial: %v", mode, err)
	}
	defer client.Close()

	var want string
	switch mode {
	case "valid":
		if _, err := client.Write(mustJSON(t, baseValid())); err != nil {
			t.Fatalf("[%s] write: %v", mode, err)
		}
		want = StateActiveLogOnly
	case "stale":
		if _, err := client.Write(mustJSON(t, baseValid())); err != nil {
			t.Fatalf("[%s] write: %v", mode, err)
		}
		waitState(t, r, StateActiveLogOnly, mode)
		// Let the last valid packet age past the 300 ms receive-time bound.
		time.Sleep(time.Duration(DefaultStaleMs)*time.Millisecond + 150*time.Millisecond)
		want = StateStale
	case "invalid":
		if _, err := client.Write([]byte("{ not json")); err != nil {
			t.Fatalf("[%s] write: %v", mode, err)
		}
		if _, err := client.Write(bytes.Repeat([]byte("A"), MaxPacketBytes+16)); err != nil {
			t.Fatalf("[%s] write oversized: %v", mode, err)
		}
		want = StateInvalid
	default:
		t.Fatalf("unknown mode %q", mode)
	}

	waitState(t, r, want, mode)

	// The receiver has run and reached its observable state; pack now, while live.
	out := packSnapshots(snaps)

	d := r.Diagnostics()
	switch mode {
	case "valid", "stale":
		if d.Counts.Valid == 0 {
			t.Fatalf("[%s] receiver processed no valid packet (vacuous): %+v", mode, d)
		}
	case "invalid":
		if d.Counts.Invalid == 0 {
			t.Fatalf("[%s] receiver processed no invalid packet (vacuous): %+v", mode, d)
		}
	}
	return out
}

func dumpHex(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(hex.EncodeToString(b)+"\n"), 0o644); err != nil {
		t.Fatalf("dump %s: %v", p, err)
	}
	t.Logf("wrote %s (%d CRSF bytes)", p, len(b))
}

// TestPackChannelsUnchangedByReceiver is the byte-for-byte dead-end proof.
func TestPackChannelsUnchangedByReceiver(t *testing.T) {
	snaps := gamepadSnapshots()

	baseline := packSnapshots(snaps) // FLAG OFF: no receiver is ever constructed
	if len(baseline) == 0 {
		t.Fatal("empty baseline pack output")
	}

	onValid := packWithReceiver(t, snaps, "valid")
	onStale := packWithReceiver(t, snaps, "stale")
	onInvalid := packWithReceiver(t, snaps, "invalid")

	for _, c := range []struct {
		name string
		got  []byte
	}{
		{"on/valid", onValid},
		{"on/stale", onStale},
		{"on/invalid", onInvalid},
	} {
		if !bytes.Equal(baseline, c.got) {
			t.Errorf("CRSF bytes differ flag-off vs %s (dead end violated)\n off  =%s\n %s=%s",
				c.name, hex.EncodeToString(baseline), c.name, hex.EncodeToString(c.got))
		}
	}

	t.Logf("gamepad->CRSF pack output identical across off / on-valid / on-stale / on-invalid "+
		"(%d bytes, %d snapshots)", len(baseline), len(snaps))

	// Optional shell-diffable artifact: set HEADINTENT_PACK_DUMP=<dir>.
	if dir := os.Getenv("HEADINTENT_PACK_DUMP"); dir != "" {
		dumpHex(t, dir, "pack_off.hex", baseline)
		dumpHex(t, dir, "pack_on_valid.hex", onValid)
		dumpHex(t, dir, "pack_on_stale.hex", onStale)
		dumpHex(t, dir, "pack_on_invalid.hex", onInvalid)
	}
}

// TestPackChannelsUnchangedByDiagnosticsSubscribers extends the dead-end proof to
// slice 3A: a running receiver PLUS a running diagnostics Broadcaster with several
// subscribers (an active drainer, a stuck/slow one that never reads, and one that
// disconnects) — under valid, invalid, and stale traffic — must not perturb the
// gamepad->CRSF packed output by a single byte. The broadcaster only READS the
// receiver snapshot and fans it out; it shares no state with the pack path.
func TestPackChannelsUnchangedByDiagnosticsSubscribers(t *testing.T) {
	snaps := gamepadSnapshots()
	baseline := packSnapshots(snaps) // no receiver, no broadcaster, no subscribers

	r := NewReceiver(Options{Port: 0, BindHost: "127.0.0.1"})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	bc := NewBroadcaster(r.Diagnostics, BroadcasterOptions{PollInterval: 5 * time.Millisecond})
	bc.Start()
	defer bc.Stop()

	// Active drainer.
	drain, drainUnsub, err := bc.Subscribe()
	if err != nil {
		t.Fatalf("subscribe drainer: %v", err)
	}
	defer drainUnsub()
	stopDrain := make(chan struct{})
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		for {
			select {
			case <-stopDrain:
				return
			case <-drain:
			}
		}
	}()
	defer func() { close(stopDrain); drainWG.Wait() }()

	// Stuck/slow subscriber: never reads.
	_, slowUnsub, err := bc.Subscribe()
	if err != nil {
		t.Fatalf("subscribe slow: %v", err)
	}
	defer slowUnsub()

	// Disconnecting subscriber.
	_, goneUnsub, err := bc.Subscribe()
	if err != nil {
		t.Fatalf("subscribe gone: %v", err)
	}
	goneUnsub()

	client, err := net.Dial("udp4", r.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Valid, then invalid traffic; then let it go stale — exercising the fan-out.
	if _, err := client.Write(mustJSON(t, baseValid())); err != nil {
		t.Fatalf("write valid: %v", err)
	}
	waitState(t, r, StateActiveLogOnly, "subs/valid")
	pmid := packSnapshots(snaps)

	if _, err := client.Write([]byte("{ not json")); err != nil {
		t.Fatalf("write invalid: %v", err)
	}
	if _, err := client.Write(bytes.Repeat([]byte("A"), MaxPacketBytes+16)); err != nil {
		t.Fatalf("write oversized: %v", err)
	}
	time.Sleep(time.Duration(DefaultStaleMs)*time.Millisecond + 150*time.Millisecond)
	waitState(t, r, StateStale, "subs/stale")
	pstale := packSnapshots(snaps)

	for name, got := range map[string][]byte{"during-valid": pmid, "after-stale": pstale} {
		if !bytes.Equal(baseline, got) {
			t.Errorf("CRSF bytes changed with diagnostics subscribers active (%s)", name)
		}
	}

	d := r.Diagnostics()
	if d.Counts.Valid == 0 || d.Counts.Invalid == 0 {
		t.Fatalf("receiver did not process both valid and invalid traffic: %+v", d)
	}
}
