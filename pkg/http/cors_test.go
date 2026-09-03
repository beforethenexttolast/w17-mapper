// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package http

// CORS tests for the grpc-web port (review finding MAP-8, the residue the
// loopback default left open).
//
// The threat is not a host on the network -- loopback removed those. It is the
// browser on the SAME PC: a page on any web site the operator has open could
// call SetConfig, StartLink, StopLink and SetCRSFDeviceField on
// http://127.0.0.1:3000, unauthenticated, because every response said
// Access-Control-Allow-Origin: * .
//
// These drive the REAL handler NewEcho builds, through httptest, so what is
// asserted is what a browser would actually receive.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveWith runs one request against the handler NewEcho builds for the given
// bind host, and returns the response.
func serveWith(t *testing.T, bindHost, requestHost, origin string) *http.Response {
	t.Helper()

	e, err := newTestCtl(bindHost, false).NewEcho(nil)
	if err != nil {
		t.Fatalf("NewEcho: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://"+requestHost+"/", nil)
	req.Host = requestHost
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	rec := httptest.NewRecorder()
	e.Server.Handler.ServeHTTP(rec, req)
	return rec.Result()
}

// TestAForeignOriginGetsNoPermission is the fix. A page on any other site must
// not be told it may read the answers.
func TestAForeignOriginGetsNoPermission(t *testing.T) {
	for _, origin := range []string{
		"http://evil.example",
		"https://evil.example",
		"http://127.0.0.1:8080",   // right host, wrong port
		"http://192.168.1.9:3000", // right port, wrong host
		"null",
	} {
		res := serveWith(t, "127.0.0.1", "127.0.0.1:3000", origin)
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q was granted %q -- any page in a browser on this PC "+
				"could then call SetConfig, StartLink, StopLink and SetCRSFDeviceField",
				origin, got)
		}
		if got := res.Header.Get("Access-Control-Allow-Headers"); got != "" {
			t.Errorf("origin %q was granted Access-Control-Allow-Headers %q", origin, got)
		}
	}
}

// TestTheWebUIsOwnOriginIsAllowed: whichever of the three loopback names the
// operator typed, the editor is the UI this server serves and must keep working.
func TestTheWebUIsOwnOriginIsAllowed(t *testing.T) {
	for _, tc := range []struct{ host, origin string }{
		{"127.0.0.1:3000", "http://127.0.0.1:3000"},
		{"localhost:3000", "http://localhost:3000"},
		{"[::1]:3000", "http://[::1]:3000"},
	} {
		res := serveWith(t, "127.0.0.1", tc.host, tc.origin)
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != tc.origin {
			t.Errorf("the UI's own origin %q was answered with %q, want the origin echoed back",
				tc.origin, got)
		}
	}
}

// TestARequestWithNoOriginIsUntouched. A same-origin GET (and anything that is
// not a browser) sends no Origin and needs no CORS header; the response must
// not grow one.
func TestARequestWithNoOriginIsUntouched(t *testing.T) {
	res := serveWith(t, "127.0.0.1", "127.0.0.1:3000", "")

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a request with no Origin was answered with Access-Control-Allow-Origin %q", got)
	}
	if !strings.Contains(res.Header.Get("Vary"), "Origin") {
		t.Error("Vary: Origin must be set on every response, or a cache can hand one " +
			"origin another origin's permission")
	}
}

// TestBindAllFallsBackToSameOrigin. With -bind-all the operator has said "serve
// this to my network" and the machine cannot know which name they will type, so
// the rule becomes same-origin by authority -- which still refuses every
// cross-origin page.
func TestBindAllFallsBackToSameOrigin(t *testing.T) {
	res := serveWith(t, "", "192.168.1.9:3000", "http://192.168.1.9:3000")
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "http://192.168.1.9:3000" {
		t.Errorf("-bind-all: the UI's own origin was answered with %q, want it echoed", got)
	}

	res = serveWith(t, "", "192.168.1.9:3000", "http://evil.example")
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("-bind-all: a foreign origin was granted %q", got)
	}
}

// TestTheAllowlistIsNotSameOriginByHost pins the reason the loopback path uses
// an allowlist instead of comparing Origin with Host: a DNS-rebinding page
// resolves attacker.tld to 127.0.0.1, so its Origin and Host agree with each
// other and a same-origin rule would admit it.
func TestTheAllowlistIsNotSameOriginByHost(t *testing.T) {
	res := serveWith(t, "127.0.0.1", "attacker.example:3000", "http://attacker.example:3000")

	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a rebinding origin was granted %q -- the loopback rule must be an "+
			"allowlist, not Origin == Host", got)
	}
}

// TestTheAllowlistNamesEveryLocalName is the unit behind the handler tests.
func TestTheAllowlistNamesEveryLocalName(t *testing.T) {
	got := allowedOrigins("127.0.0.1", 3000)

	for _, want := range []string{
		"http://127.0.0.1:3000", "http://localhost:3000", "http://[::1]:3000",
	} {
		found := false
		for _, o := range got {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the allowlist has no %s: %v", want, got)
		}
	}

	if allowedOrigins("", 3000) != nil {
		t.Error("-bind-all must produce no allowlist, so the same-origin fallback runs")
	}

	// A non-loopback -bind-host is taken at its word and gets exactly itself.
	if got := allowedOrigins("192.168.1.9", 3000); len(got) != 2 ||
		got[0] != "http://192.168.1.9:3000" {
		t.Errorf("an explicit -bind-host produced %v", got)
	}
}
