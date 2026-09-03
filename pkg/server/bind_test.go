// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package server

// Bind-policy tests for the gRPC port (MAP-8, owner decision OD-8(a)).
//
// Upstream listened on the wildcard: :10000 with SERVER REFLECTION on, no
// credentials and no interceptor, exposing SetConfig / StartLink / StopLink /
// SetCRSFDeviceField to anything that could reach the machine -- which race day
// now makes the host of the phone's hotspot. CURRENT_STATUS recorded the
// wildcard as "left unchanged per owner; tightening it is a separate decision",
// i.e. deferred, never ratified; OD-8(a) closes it.
//
// Every legitimate client is already local (pkg/client dials the loopback
// address, the ground station dials 127.0.0.1:10000), so the loopback default
// costs nothing and -bind-all is the whole of the way back.

import (
	"net"
	"testing"
)

func TestDefaultBindHostIsLoopback(t *testing.T) {
	ip := net.ParseIP(DefaultBindHost)
	if ip == nil {
		t.Fatalf("DefaultBindHost %q is not an IP address", DefaultBindHost)
	}
	if !ip.IsLoopback() {
		t.Errorf("DefaultBindHost = %q, which is not loopback -- the unauthenticated "+
			"gRPC surface would be reachable from the hotspot", DefaultBindHost)
	}
}

func TestListenAddrJoinsHostAndPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		port int
		want string
	}{
		{"loopback default", DefaultBindHost, 10000, "127.0.0.1:10000"},
		{"explicit -bind-all", "", 10000, ":10000"},
		{"an IPv6 host is bracketed", "::1", 10000, "[::1]:10000"},
	} {
		if got := listenAddr(tc.host, tc.port); got != tc.want {
			t.Errorf("%s: listenAddr(%q, %d) = %q, want %q", tc.name, tc.host, tc.port, got, tc.want)
		}
	}
}

// TestTheDefaultActuallyBindsLoopbackOnly opens a real listener on the address
// the server composes and asks the kernel what it bound -- the assertion the
// string tests above cannot make.
func TestTheDefaultActuallyBindsLoopbackOnly(t *testing.T) {
	lis, err := net.Listen("tcp", listenAddr(DefaultBindHost, 0))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	addr, ok := lis.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected address type %T", lis.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Errorf("the default bound %s, which is not loopback", addr.IP)
	}
	if addr.IP.IsUnspecified() {
		t.Errorf("the default bound the wildcard %s", addr.IP)
	}
}

// TestBindAllIsStillReachable is the guard against over-correcting: the
// hobbyist path must still be able to ask for every interface.
func TestBindAllIsStillReachable(t *testing.T) {
	lis, err := net.Listen("tcp", listenAddr("", 0))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	addr, ok := lis.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected address type %T", lis.Addr())
	}
	if !addr.IP.IsUnspecified() {
		t.Errorf("-bind-all bound %s, want the wildcard", addr.IP)
	}
}
