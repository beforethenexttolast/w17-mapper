// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package main

import (
	"flag"
	"fmt"
	"github.com/kaack/elrs-joystick-control/pkg/client"
	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	hi "github.com/kaack/elrs-joystick-control/pkg/headintent"
	hc "github.com/kaack/elrs-joystick-control/pkg/http"
	lc "github.com/kaack/elrs-joystick-control/pkg/link"
	sc "github.com/kaack/elrs-joystick-control/pkg/serial"
	gc "github.com/kaack/elrs-joystick-control/pkg/server"
	"github.com/kaack/elrs-joystick-control/webapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"os"
	"os/signal"
	"strconv"
	"strings"
)

// envTruthy reports whether the named environment variable is set to a truthy
// value (1/t/T/TRUE/true/… per strconv.ParseBool). Used only to pick the default
// for the disabled-by-default -headtrack-ingest flag; an explicit CLI flag wins.
func envTruthy(name string) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

func main() {

	// W17 fork modification: the process exit status is set here and applied by
	// the FIRST deferred call, which -- defers being LIFO -- therefore runs LAST,
	// after every controller below has shut down. A headless bring-up failure
	// (an unreadable or unfilled profile, a COM port that is not there) must
	// leave a non-zero status for the ground station to see, and must still
	// unwind the controllers cleanly on the way out. os.Exit skips defers, so it
	// cannot be called at the failure site itself.
	exitCode := 0
	defer func() { os.Exit(exitCode) }()

	webAppPort := new(int)
	flag.IntVar(webAppPort, "webapp-port", 3000, "Web Application port number")

	grpcPort := new(int)
	flag.IntVar(grpcPort, "grpc-port", 10000, "gRPC Server port number")

	txServerPortName := new(string)
	flag.StringVar(txServerPortName, "tx-serial-port-name", "", "tx Serial port name")

	txServerPortBaudRate := new(int)
	flag.IntVar(txServerPortBaudRate, "tx-serial-port-baud-rate", 921600, "tx Serial port baud rate")

	configFilePath := new(string)
	flag.StringVar(configFilePath, "config-file-path", "", "config json file path")

	disableWebUI := new(bool)
	flag.BoolVar(disableWebUI, "disable-web-ui", false, "disable the Web-UI HTTP server")

	// W17 owned-fork addition (MAP-8, owner decision OD-8(a)): both listeners
	// bind LOOPBACK by default. Upstream bound the wildcard on both -- gRPC
	// with server reflection, and the grpc-web UI with CORS * -- unauthenticated
	// and with no interceptor, on the machine that race day now makes the host
	// of the phone's hotspot. Every legitimate client is local, so the default
	// costs nothing; -bind-all is the one-flag way back for the hobbyist path
	// where the mapper UI is opened from another machine.
	bindHost := new(string)
	flag.StringVar(bindHost, "bind-host", gc.DefaultBindHost,
		"interface the gRPC and Web-UI ports listen on (loopback by default)")

	bindAll := new(bool)
	flag.BoolVar(bindAll, "bind-all", false,
		"listen on EVERY interface instead of loopback -- the gRPC and Web-UI "+
			"ports are unauthenticated, so only do this on a network you trust")

	// W17 owned-fork addition (MAP-8): the runtime profiling handlers are off
	// unless asked for. A CPU profile request is enough to stall the process
	// driving the car, and they were mounted unconditionally.
	enablePprof := new(bool)
	flag.BoolVar(enablePprof, "pprof", false,
		"expose Go runtime profiling handlers on the Web-UI port (diagnostics only)")

	// W17 owned-fork addition (not upstream): disabled-by-default, LOG-ONLY
	// head-intent receiver. Default comes from env W17_HEADTRACK_INGEST so it stays
	// off unless explicitly opted in; an explicit -headtrack-ingest flag overrides.
	headTrackIngest := new(bool)
	flag.BoolVar(headTrackIngest, "headtrack-ingest", envTruthy("W17_HEADTRACK_INGEST"),
		"EXPERIMENTAL, LOG-ONLY: start the head-intent UDP receiver (W3, port 5602). "+
			"Disabled by default. Produces only log lines and a read-only diagnostics "+
			"snapshot; it never reaches the node graph, CRSF, servos, or the gimbal "+
			"(env W17_HEADTRACK_INGEST).")

	headTrackPort := new(int)
	flag.IntVar(headTrackPort, "headtrack-port", hi.DefaultPort,
		"UDP port for the LOG-ONLY head-intent receiver (only used with -headtrack-ingest)")

	flag.Parse()

	// -bind-all wins over -bind-host: an empty host is what net.JoinHostPort
	// renders as ":port", i.e. every interface.
	listenHost := *bindHost
	if *bindAll {
		listenHost = ""
		fmt.Println("(bind) -bind-all: the gRPC and Web-UI ports will listen on EVERY " +
			"interface, unauthenticated -- only do this on a network you trust")
	}

	// The headless bring-up talks to this same process, so it dials whatever
	// this process bound; with -bind-all it dials loopback, which is always
	// reachable from here.
	clientHost := listenHost
	if clientHost == "" {
		clientHost = gc.DefaultBindHost
	}

	// --- W17 owned-fork addition: LOG-ONLY head-intent receiver (UDP 5602, W3) ---
	// When enabled this starts a headintent.Receiver plus a read-only diagnostics
	// Broadcaster and NOTHING else on the control side: the receiver binds the UDP
	// socket and logs; the broadcaster only READS the receiver's snapshot and fans
	// it out to the WatchHeadIntentDiagnostics gRPC stream (mapper->Electron,
	// one-way). Neither is passed to devicesCtl, configCtl, serialCtl, linkCtl, or
	// client.Init, so no value the receiver sees can reach the node graph, the
	// [16]CRSFValue arrays, the CRSF/link send path, ch9/10, servos, or the gimbal
	// (see pkg/headintent/doc.go). A bind failure is logged and ignored. When
	// disabled (the default) no receiver, socket, or broadcaster is created and the
	// diagnostics RPC reports Unavailable, so the control path is byte-for-byte
	// identical to upstream.
	var hiBroadcaster *hi.Broadcaster
	if *headTrackIngest {
		hiRcv := hi.NewReceiver(hi.Options{
			Port: *headTrackPort,
			Log:  func(s string) { fmt.Println(s) },
		})
		if err := hiRcv.Start(); err != nil {
			fmt.Printf("[headtrack] LOG-ONLY receiver did not start (ignored): %s\n", err.Error())
		}
		defer hiRcv.Stop()

		hiBroadcaster = hi.NewBroadcaster(hiRcv.Diagnostics, hi.BroadcasterOptions{})
		hiBroadcaster.Start()
		defer hiBroadcaster.Stop()
	}

	grpcServer := grpc.NewServer([]grpc.ServerOption{}...)
	reflection.Register(grpcServer)

	// W17 fork modification: the built web bundle is resolved HERE and handed to
	// the HTTP controller, rather than imported by it. That keeps the embedded
	// bundle -- which only exists after `go generate ./...` has run npm and
	// webpack -- out of the build graph of everything except this binary.
	webAssets, err := webapp.HTTPFileSystem()
	if err != nil {
		fmt.Printf("\ncould not open the built web UI bundle: %s\n\n", err.Error())
		exitCode = 1
		return
	}

	httpCtl := hc.NewCtl(listenHost, *webAppPort, grpcServer, webAssets, *enablePprof)
	defer httpCtl.Quit()

	devicesCtl := dc.NewCtl()
	defer devicesCtl.Quit()

	configCtl := cc.NewCtl(devicesCtl)

	defer configCtl.Quit()

	serialCtl := sc.NewCtl()
	defer serialCtl.Quit()

	linkCtl := lc.NewCtl(devicesCtl, serialCtl, configCtl)
	defer linkCtl.Quit()

	serverCtl := gc.NewCtl(listenHost, *grpcPort, grpcServer, devicesCtl, serialCtl, configCtl, linkCtl, httpCtl, hiBroadcaster)
	defer serverCtl.Quit()

	// Automatically configure through gprc when conditions are met.
	// W17 fork modification: Init returns an error instead of panicking, so a
	// bring-up failure prints one readable line rather than a goroutine dump.
	if err := client.Init(clientHost, *txServerPortName, *configFilePath, *txServerPortBaudRate, *grpcPort, *disableWebUI); err != nil {
		fmt.Printf("\n%s\n\n", err.Error())
		exitCode = 1
		return
	}

	go func() {
		sigChan := make(chan os.Signal)
		signal.Notify(sigChan, os.Interrupt)
		<-sigChan
		fmt.Println("Ctrl-C detected, exiting")
		if err := httpCtl.Stop(); err != nil {
			fmt.Printf("could not stop HTTP controller. %s\n", err.Error())
		}
		if err := serverCtl.Stop(); err != nil {
			fmt.Printf("could not stop HTTP controller. %s\n", err.Error())
		}
	}()

	serverCtl.Wait()
}
