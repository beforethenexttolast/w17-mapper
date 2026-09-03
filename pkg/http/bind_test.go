// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package http

// Bind-policy and pprof tests for the Web-UI port (MAP-8, owner decision
// OD-8(a)).
//
// :3000 served the grpc-web bridge with Access-Control-Allow-Origin *, plus the
// Go runtime profiling handlers, on every interface, unauthenticated. The
// cheapest abuse needs no arming interlock defeated at all: one CPU-profile
// request stalls the process that is driving the car.

import (
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

func newTestCtl(bindHost string, pprof bool) *Controller {
	return &Controller{
		webAppPort:  3000,
		bindHost:    bindHost,
		gRPCServer:  grpc.NewServer(),
		enablePprof: pprof,
	}
}

func TestWebUIBindsTheGivenHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		want string
	}{
		{"loopback default", "127.0.0.1", "127.0.0.1:3000"},
		{"explicit -bind-all", "", ":3000"},
	} {
		e, err := newTestCtl(tc.host, false).NewEcho(nil)
		if err != nil {
			t.Fatalf("%s: NewEcho: %v", tc.name, err)
		}
		if got := e.Server.Addr; got != tc.want {
			t.Errorf("%s: server address = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestTheWebUIDefaultActuallyBindsLoopback asks the kernel rather than the
// string.
func TestTheWebUIDefaultActuallyBindsLoopback(t *testing.T) {
	e, err := newTestCtl("127.0.0.1", false).NewEcho(nil)
	if err != nil {
		t.Fatalf("NewEcho: %v", err)
	}

	host, _, err := net.SplitHostPort(e.Server.Addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", e.Server.Addr, err)
	}
	lis, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	addr := lis.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() {
		t.Errorf("the Web-UI default bound %s, which is not loopback", addr.IP)
	}
}

// TestPprofIsOffByDefault is the second half of OD-8(a). The profiling routes
// must not exist unless -pprof was passed.
func TestPprofIsOffByDefault(t *testing.T) {
	e, err := newTestCtl("127.0.0.1", false).NewEcho(nil)
	if err != nil {
		t.Fatalf("NewEcho: %v", err)
	}

	for _, route := range e.Routes() {
		if strings.Contains(route.Path, "/debug/pprof") {
			t.Errorf("pprof route %s %s is registered with -pprof off", route.Method, route.Path)
		}
	}
}

// TestPprofCanStillBeAskedFor keeps the diagnostics available deliberately --
// the fix is a gate, not a removal.
func TestPprofCanStillBeAskedFor(t *testing.T) {
	e, err := newTestCtl("127.0.0.1", true).NewEcho(nil)
	if err != nil {
		t.Fatalf("NewEcho: %v", err)
	}

	for _, route := range e.Routes() {
		if strings.Contains(route.Path, "/debug/pprof") {
			return
		}
	}
	t.Error("-pprof registered no profiling routes")
}
