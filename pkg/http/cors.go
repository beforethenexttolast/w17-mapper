// SPDX-FileCopyrightText: © 2026 W17 project (owned fork addition)
// SPDX-License-Identifier: GPL-3.0-or-later

package http

// Who the grpc-web port will answer for (review finding MAP-8, the residue
// branch A's loopback default deliberately left open).
//
// WHAT WAS WRONG. Every response on the web-UI port carried
// `Access-Control-Allow-Origin: *` and `Access-Control-Allow-Headers: *`,
// unconditionally. Binding loopback (OD-8(a)) removed every host on the
// network, but it does not remove the browser sitting on the same PC: a page on
// any web site the operator has open can issue grpc-web calls to
// http://127.0.0.1:3000, and the wildcard tells that browser to hand it the
// answers. The reachable calls are SetConfig, StartLink, StopLink and
// SetCRSFDeviceField, with no credentials, on the machine that is driving the
// car. Both READMEs said so in those words; this closes it.
//
// THE RULE. The header is sent only for an Origin that is the web UI's own.
// With the loopback default that is 127.0.0.1 / localhost / [::1] on the
// configured web-UI port -- an explicit allowlist rather than "same origin as
// the request", because a same-origin rule keyed on the Host header would still
// admit DNS rebinding, where a page on attacker.tld resolves to 127.0.0.1 and
// its Origin and Host agree with each other.
//
// WHY THIS CANNOT BREAK THE EDITOR. The node-graph UI is served by this same
// server on this same port, so its grpc-web calls are SAME-ORIGIN: its Origin
// is in the allowlist, and a same-origin request needs no CORS headers at all
// and triggers no preflight. Nothing the local UI does depends on the wildcard.
// The ground station is not affected either way -- it speaks native gRPC to
// :10000 from Node, not grpc-web from a browser.
//
// -bind-all is the one case an allowlist cannot enumerate: the operator has
// deliberately said "serve this UI to my network", and the machine cannot know
// which name they will type. There the rule falls back to same-origin by Host,
// which still refuses every cross-origin page and leaves only the rebinding
// case, on a path whose flag help already says "only do this on a network you
// trust".

import (
	"net"
	"net/url"
	"strconv"
)

// loopbackHosts are the names a browser on this PC can reach the web UI by when
// it is bound to loopback. All three resolve to this machine and no other.
var loopbackHosts = []string{"127.0.0.1", "localhost", "::1"}

// allowedOrigins is the exact set of Origin values the web-UI port will answer
// for, or nil when the bind host is the wildcard (-bind-all), where the rule
// falls back to same-origin.
func allowedOrigins(bindHost string, webAppPort int) []string {
	if bindHost == "" {
		return nil
	}

	hosts := []string{bindHost}
	if isLoopbackHost(bindHost) {
		hosts = loopbackHosts
	}

	port := strconv.Itoa(webAppPort)
	origins := make([]string, 0, 2*len(hosts))
	for _, host := range hosts {
		authority := net.JoinHostPort(host, port)
		// Both schemes: this server speaks http, but a reverse proxy in front
		// of it on the same authority would present https, and refusing that
		// would be a surprise with no security value -- the authority is what
		// identifies the UI.
		origins = append(origins, "http://"+authority, "https://"+authority)
	}
	return origins
}

// isLoopbackHost reports whether a bind host names this machine's loopback.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// originAllowed decides whether to echo an Origin back.
//
// An empty Origin is NOT allowed -- and needs nothing: a request without one is
// same-origin or not from a browser, and either way no CORS header applies.
func originAllowed(origin, requestHost string, allowed []string) bool {
	if origin == "" || origin == "null" {
		return false
	}

	for _, candidate := range allowed {
		if origin == candidate {
			return true
		}
	}

	// -bind-all only: same-origin by authority.
	if len(allowed) == 0 {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" {
			return false
		}
		return parsed.Host == requestHost
	}

	return false
}
