// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package http

// Tests for the web-UI shutdown path (MAP-11).
//
// The defect: the shutdown goroutine called echo.Shutdown(nil). net/http's
// Server.Shutdown selects on ctx.Done(), and Done() on a nil context is a
// nil-interface dereference, so any shutdown with a non-idle connection open
// panicked -- inside a tomb goroutine, where a panic is a PROCESS fault. It
// never reached the normal exit path (httpCtl.Quit is deferred FIRST in main
// and therefore runs LAST, after every other controller has already stopped),
// so the realistic symptom was a goroutine dump instead of a clean exit on the
// Ctrl-C path. The ground station reads mapper stdout/stderr into its
// diagnostics ring, so that dump is what an operator would have been shown for
// a normal stop.

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeServer struct {
	shutdownCtx context.Context
	shutdownErr error
	panicWith   any
	closed      bool
}

func (f *fakeServer) Shutdown(ctx context.Context) error {
	f.shutdownCtx = ctx
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.shutdownErr
}

func (f *fakeServer) Close() error {
	f.closed = true
	return nil
}

// TestShutdownPassesARealContext is the defect itself: a nil context is what
// panicked, so the contract is that a real one with a deadline is passed.
func TestShutdownPassesARealContext(t *testing.T) {
	server := &fakeServer{}

	if err := shutdownEcho(server); err != nil {
		t.Fatalf("a clean shutdown must not report an error: %v", err)
	}

	if server.shutdownCtx == nil {
		t.Fatal("Shutdown was handed a nil context -- this is the MAP-11 panic")
	}
	deadline, ok := server.shutdownCtx.Deadline()
	if !ok {
		t.Fatal("the shutdown context must have a deadline, or a stuck connection hangs the exit")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > shutdownTimeout {
		t.Errorf("deadline is %v away, want (0, %v]", remaining, shutdownTimeout)
	}
}

// TestShutdownForcesCloseOnDeadline covers the other half: a graceful shutdown
// that does not finish must not leave the process waiting on a client.
func TestShutdownForcesCloseOnDeadline(t *testing.T) {
	server := &fakeServer{shutdownErr: context.DeadlineExceeded}

	if err := shutdownEcho(server); err != nil {
		t.Errorf("a forced close is not a failure to report: %v", err)
	}
	if !server.closed {
		t.Error("a shutdown that timed out must be followed by a forced close")
	}
}

// TestShutdownRecoversFromAPanic is the invariant that matters: whatever the
// HTTP stack does on the way out, it cannot be a process fault. Without the
// recover this test crashes the whole run rather than failing.
func TestShutdownRecoversFromAPanic(t *testing.T) {
	server := &fakeServer{panicWith: errors.New("runtime error: invalid memory address")}

	if err := shutdownEcho(server); err != nil {
		t.Errorf("a recovered shutdown fault must not be reported as an error: %v", err)
	}
}

// TestShutdownOfANilServerIsHarmless covers Stop being reached before the
// server was ever built.
func TestShutdownOfANilServerIsHarmless(t *testing.T) {
	if err := shutdownEcho(nil); err != nil {
		t.Errorf("shutting down nothing must be a no-op, got %v", err)
	}
}
