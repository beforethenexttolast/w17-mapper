// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"errors"
	"net"
	"testing"
	"time"
)

func fixedNow() func() time.Time { return func() time.Time { return at(0) } }

func TestReceiverDisabledByDefault(t *testing.T) {
	r := NewReceiver(Options{Now: fixedNow()})
	if got := r.State(at(0)); got != StateDisabled {
		t.Errorf("state before Start = %q, want disabled", got)
	}
	if d := r.Diagnostics(); d.State != StateDisabled {
		t.Errorf("diagnostics state = %q, want disabled", d.State)
	}
}

func TestReceiverFaultOnBindError(t *testing.T) {
	boom := errors.New("bind boom")
	r := NewReceiver(Options{
		Now:    fixedNow(),
		Listen: func(string, string) (net.PacketConn, error) { return nil, boom },
	})
	err := r.Start()
	if err == nil {
		t.Fatal("Start should return the bind error")
	}
	if got := r.State(at(0)); got != StateFault {
		t.Errorf("state = %q, want fault", got)
	}
	if r.LastError() != boom.Error() {
		t.Errorf("lastError = %q, want %q", r.LastError(), boom.Error())
	}
}

func TestReceiverAcceptsRealDatagram(t *testing.T) {
	r := NewReceiver(Options{Port: 0, BindHost: "127.0.0.1", Now: fixedNow()})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	client, err := net.Dial("udp4", r.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Write(mustJSON(t, baseValid())); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Poll until the goroutine has processed the datagram.
	deadline := time.Now().Add(2 * time.Second)
	for {
		d := r.Diagnostics()
		if d.Counts.Valid == 1 {
			if d.State != StateActiveLogOnly {
				t.Errorf("state = %q, want active_log_only", d.State)
			}
			if d.LastValid == nil || d.LastValid.Seq != 1 {
				t.Errorf("lastValid = %+v", d.LastValid)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("datagram not processed in time; diagnostics=%+v", d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Proves the mapper and Electron cannot both claim UDP 5602: a plain exclusive
// bind (no SO_REUSEPORT) means a second bind on the same address fails. This is
// the same exclusivity the Electron receiver relies on, so the two receiver
// modes are mutually exclusive by the OS, not just by policy.
func TestUDPPortIsExclusive(t *testing.T) {
	// First holder binds an ephemeral port on loopback.
	first, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer first.Close()
	port := first.LocalAddr().(*net.UDPAddr).Port

	// A receiver aimed at the SAME address must fault, not silently share it.
	r := NewReceiver(Options{Port: port, BindHost: "127.0.0.1", Now: fixedNow()})
	err = r.Start()
	if err == nil {
		r.Stop()
		t.Fatal("second bind on the same UDP port should fail (exclusive)")
	}
	if got := r.State(at(0)); got != StateFault {
		t.Errorf("state = %q, want fault", got)
	}

	// And once the first holder releases the port, a receiver can bind it —
	// confirming the exclusivity is the port, not a defect.
	first.Close()
	r2 := NewReceiver(Options{Port: port, BindHost: "127.0.0.1", Now: fixedNow()})
	if err := r2.Start(); err != nil {
		t.Fatalf("bind after release should succeed, got %v", err)
	}
	r2.Stop()
}

func TestReceiverStopReturnsToDisabled(t *testing.T) {
	r := NewReceiver(Options{Port: 0, BindHost: "127.0.0.1", Now: fixedNow()})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Stop()
	if got := r.State(at(0)); got != StateDisabled {
		t.Errorf("state after Stop = %q, want disabled", got)
	}
}
