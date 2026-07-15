// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package headintent

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// Receiver-lifecycle states layered on top of the packet-derived states.
const (
	StateDisabled = "disabled" // receiver not running (the default)
	StateFault    = "fault"    // socket/config error
)

// DefaultPort is the canonical W3 head-intent UDP port.
const DefaultPort = 5602

// readBufferBytes is large enough to read any UDP datagram whole, so oversized
// packets (> MaxPacketBytes) are seen in full and rejected rather than silently
// truncated into something that might parse.
const readBufferBytes = 65535

// ListenFunc opens a packet connection; injectable for tests.
type ListenFunc func(network, address string) (net.PacketConn, error)

// Options configure a Receiver. The zero value is usable via sensible defaults.
type Options struct {
	Port     int              // default DefaultPort (5602)
	BindHost string           // default "0.0.0.0"
	StaleMs  int64            // default DefaultStaleMs (300)
	Now      func() time.Time // default time.Now (monotonic)
	Log      func(string)     // default no-op
	Listen   ListenFunc       // default net.ListenPacket
}

// Receiver is a LOG-ONLY UDP head-intent receiver. It owns a monitor and a read
// goroutine; it exposes only read-only diagnostics. It never touches any
// control/output path (see package doc). Disabled until Start is called.
type Receiver struct {
	port     int
	bindHost string
	now      func() time.Time
	log      func(string)
	listen   ListenFunc
	monitor  *Monitor

	mu        sync.Mutex
	conn      net.PacketConn
	running   bool
	stopping  bool
	fault     bool
	lastError string
	lastState string
	wg        sync.WaitGroup
}

// NewReceiver builds a disabled receiver. Call Start to bind the socket.
func NewReceiver(opts Options) *Receiver {
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}
	if opts.BindHost == "" {
		opts.BindHost = "0.0.0.0"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	if opts.Listen == nil {
		opts.Listen = net.ListenPacket
	}
	return &Receiver{
		port:      opts.Port,
		bindHost:  opts.BindHost,
		now:       opts.Now,
		log:       opts.Log,
		listen:    opts.Listen,
		monitor:   NewMonitor(opts.StaleMs),
		lastState: StateDisabled,
	}
}

// Start binds the UDP socket and launches the non-blocking read loop. On bind
// failure the receiver enters the fault state and returns the error; it does not
// panic and does not affect anything else in the process. Idempotent.
func (r *Receiver) Start() error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil
	}
	addr := net.JoinHostPort(r.bindHost, fmt.Sprintf("%d", r.port))
	conn, err := r.listen("udp4", addr)
	if err != nil {
		r.fault = true
		r.lastError = err.Error()
		r.lastState = StateFault
		r.mu.Unlock()
		r.log(fmt.Sprintf("[headtrack] FAULT: could not bind %s: %v", addr, err))
		return err
	}
	r.conn = conn
	r.running = true
	r.fault = false
	r.stopping = false
	r.lastState = StateIdle
	r.mu.Unlock()

	r.log(fmt.Sprintf("[headtrack] LOG-ONLY receiver listening on %s "+
		"(stale > %d ms; produces no control output)", addr, r.monitor.staleMs))

	r.wg.Add(1)
	go r.readLoop(conn)
	return nil
}

func (r *Receiver) readLoop(conn net.PacketConn) {
	defer r.wg.Done()
	buf := make([]byte, readBufferBytes)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			r.mu.Lock()
			stopping := r.stopping
			if !stopping {
				r.fault = true
				r.lastError = err.Error()
			}
			r.mu.Unlock()
			if !stopping {
				r.log(fmt.Sprintf("[headtrack] FAULT: read error: %v", err))
			}
			return
		}
		now := r.now()
		res := r.monitor.Ingest(append([]byte(nil), buf[:n]...), now)
		if !res.Accepted {
			r.log(fmt.Sprintf("[headtrack] rejected packet: %s", res.Reason))
		}
		r.announceState(now)
	}
}

// announceState logs a one-line message when the observable state changes.
func (r *Receiver) announceState(now time.Time) {
	state := r.State(now)
	r.mu.Lock()
	changed := state != r.lastState
	if changed {
		prev := r.lastState
		r.lastState = state
		r.mu.Unlock()
		r.log(fmt.Sprintf("[headtrack] state: %s -> %s", prev, state))
		return
	}
	r.mu.Unlock()
}

// State returns the full observable state (receiver lifecycle + packet-derived).
func (r *Receiver) State(now time.Time) string {
	r.mu.Lock()
	fault := r.fault
	running := r.running
	r.mu.Unlock()
	if fault {
		return StateFault
	}
	if !running {
		return StateDisabled
	}
	return r.monitor.State(now)
}

// Diagnostics returns the read-only snapshot — the receiver's only data output.
func (r *Receiver) Diagnostics() Diagnostics {
	now := r.now()
	d := r.monitor.Diagnostics(now)
	d.State = r.State(now)
	r.mu.Lock()
	d.LastError = r.lastError
	r.mu.Unlock()
	return d
}

// LastError returns the most recent socket/config error text, or "".
func (r *Receiver) LastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastError
}

// LocalAddr returns the bound address, or nil if not running (useful for tests).
func (r *Receiver) LocalAddr() net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	return r.conn.LocalAddr()
}

// Stop closes the socket and waits for the read loop to exit. Idempotent.
func (r *Receiver) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.stopping = true
	conn := r.conn
	r.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	r.wg.Wait()

	r.mu.Lock()
	r.running = false
	r.conn = nil
	r.lastState = StateDisabled
	r.mu.Unlock()
}
