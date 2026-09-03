// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package server

import (
	"errors"
	"fmt"
	cc "github.com/kaack/elrs-joystick-control/pkg/config"
	dc "github.com/kaack/elrs-joystick-control/pkg/devices"
	"github.com/kaack/elrs-joystick-control/pkg/headintent"
	lc "github.com/kaack/elrs-joystick-control/pkg/link"
	"github.com/kaack/elrs-joystick-control/pkg/proto/generated/pb"
	sc "github.com/kaack/elrs-joystick-control/pkg/serial"
	"google.golang.org/grpc"
	"gopkg.in/tomb.v2"
	"net"
)

// HTTPController is the web-UI lifecycle the gRPC layer drives, as an
// INTERFACE rather than the concrete *pkg/http.Controller. W17 fork
// modification; no behaviour change -- *http.Controller satisfies it exactly.
//
// Why: pkg/http embeds the built web bundle (webapp/fs.go `//go:embed dist/*`),
// which does not exist until `go generate ./...` has run npm + webpack. Any
// package importing pkg/server therefore could not even COMPILE on a clean
// checkout, which is why pkg/server's own tests (this file's siblings) had
// never run outside a builder image, and why the headless bring-up path --
// pkg/client -> gRPC -> SetConfig/StartLink, the one race day uses -- had no
// end-to-end test at all (MAP-1). Depending on the interface removes the web
// bundle from the control path's build graph: the giftee-facing product is
// headless, so the gRPC layer has no business requiring the UI to build.
type HTTPController interface {
	Start() error
	Stop() error
}

type Controller struct {
	gRPCPort   int
	gRPCServer *grpc.Server
	devicesCtl *dc.Controller
	serialCtl  *sc.Controller
	configCtl  *cc.Controller
	linkCtl    *lc.Controller
	httpCtl    HTTPController
	headIntent *headintent.Broadcaster

	gRPCTomb *tomb.Tomb
}

// NewCtl builds the gRPC controller. headIntent is the read-only, LOG-ONLY
// head-intent diagnostics source; pass nil when head-intent ingest is disabled
// (the WatchHeadIntentDiagnostics RPC then reports Unavailable). It is a
// diagnostics consumer only and carries no control path.
func NewCtl(gRPCPort int, gRPCServer *grpc.Server, devicesCtl *dc.Controller, serialCtl *sc.Controller, configCtl *cc.Controller, linkCtl *lc.Controller, httpCtl HTTPController, headIntent *headintent.Broadcaster) *Controller {
	serverCtl := &Controller{
		gRPCPort:   gRPCPort,
		gRPCServer: gRPCServer,
		devicesCtl: devicesCtl,
		serialCtl:  serialCtl,
		configCtl:  configCtl,
		linkCtl:    linkCtl,
		httpCtl:    httpCtl,
		headIntent: headIntent,
	}

	if err := serverCtl.Init(); err != nil {
		panic(err)
	}

	return serverCtl
}

func (c *Controller) Init() (err error) {

	if err = c.Start(); err != nil {
		return errors.Join(errors.New("could not start gRPC server"), err)
	}

	return nil
}

func (c *Controller) Start() (err error) {

	c.gRPCTomb = &tomb.Tomb{}
	c.gRPCTomb.Go(func() error {

		pb.RegisterJoystickControlServer(c.gRPCServer, &GRPCServer{
			DevicesCtl: c.devicesCtl,
			SerialCtl:  c.serialCtl,
			ConfigCtl:  c.configCtl,
			LinkCtl:    c.linkCtl,
			HTTPCtl:    c.httpCtl,
			HeadIntent: c.headIntent,
		})

		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", c.gRPCPort))
		if err != nil {
			return errors.New(fmt.Sprintf("could not listen on http port %d. %v", c.gRPCPort, err))
		}

		fmt.Printf("⇨ gRPC server started on [::]:%d\n", c.gRPCPort)
		if err = c.gRPCServer.Serve(lis); err != nil {
			return errors.New(fmt.Sprintf("could not serve gRPC on port %d. %v", c.gRPCPort, err))
		}

		return nil
	})

	c.gRPCTomb.Go(func() error {
		<-c.gRPCTomb.Dying()
		c.gRPCServer.Stop()
		return nil
	})

	return nil
}

func (c *Controller) Stop() (err error) {

	if c.gRPCTomb == nil || !c.gRPCTomb.Alive() {
		return nil
	}

	c.gRPCTomb.Kill(nil)
	if err := c.gRPCTomb.Wait(); err != nil {
		return err
	}

	return nil
}

func (c *Controller) Quit() {
	if err := c.Stop(); err != nil {
		panic(fmt.Sprintf("(grpc) error while existing controller. %s\n", err.Error()))
	}
}

func (c *Controller) Wait() {
	if err := c.gRPCTomb.Wait(); err != nil {
		panic(err)
	}

}
