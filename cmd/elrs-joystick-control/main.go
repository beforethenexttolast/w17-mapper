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
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	_ "net/http/pprof"
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

	httpCtl := hc.NewCtl(*webAppPort, grpcServer)
	defer httpCtl.Quit()

	devicesCtl := dc.NewCtl()
	defer devicesCtl.Quit()

	configCtl := cc.NewCtl(devicesCtl)

	defer configCtl.Quit()

	serialCtl := sc.NewCtl()
	defer serialCtl.Quit()

	linkCtl := lc.NewCtl(devicesCtl, serialCtl, configCtl)
	defer linkCtl.Quit()

	serverCtl := gc.NewCtl(*grpcPort, grpcServer, devicesCtl, serialCtl, configCtl, linkCtl, httpCtl, hiBroadcaster)
	defer serverCtl.Quit()

	// Automatically configure through gprc when conditions are met
	client.Init(*txServerPortName, *configFilePath, *txServerPortBaudRate, *grpcPort, *disableWebUI)

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
