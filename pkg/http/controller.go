// SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
// SPDX-License-Identifier: GPL-3.0-or-later
// SPDX-License-Identifier: FS-0.9-or-later

package http

import (
	"context"
	"errors"
	"fmt"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/ttys3/echo-pprof/v4"
	"net"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"gopkg.in/tomb.v2"

	"net/http"
)

// shutdownTimeout bounds a graceful web-UI shutdown before it is forced.
// W17 fork addition (MAP-11).
const shutdownTimeout = 5 * time.Second

type Controller struct {
	webAppPort int
	httpTomb   *tomb.Tomb
	echo       *echo.Echo
	gRPCServer *grpc.Server

	// bindHost is the interface the web UI listens on; empty means every
	// interface (the explicit -bind-all path). W17 fork modification (MAP-8).
	bindHost string

	// enablePprof registers the runtime profiling handlers. W17 fork
	// modification (MAP-8, OD-8(a)): OFF unless asked for. They were mounted
	// unconditionally on a port that bound every interface with CORS *, and a
	// CPU profile request alone is enough to stall the process that is driving
	// the car.
	enablePprof bool

	// assets is the built web bundle this server serves, or nil to serve no
	// static content at all. W17 fork modification: the bundle used to be
	// imported here directly (webapp.HTTPFileSystem), which made pkg/http --
	// and every package that imported it -- impossible to build until
	// `go generate ./...` had run npm and webpack. main.go supplies it now, so
	// the lifecycle rules below can be tested on a clean checkout.
	assets http.FileSystem
}

func NewCtl(bindHost string, webAppPort int, gRPCServer *grpc.Server, assets http.FileSystem, enablePprof bool) *Controller {
	httpCtl := &Controller{
		webAppPort:  webAppPort,
		bindHost:    bindHost,
		gRPCServer:  gRPCServer,
		assets:      assets,
		enablePprof: enablePprof,
	}

	if err := httpCtl.Init(); err != nil {
		panic(err)
	}

	return httpCtl
}

func (c *Controller) Init() (err error) {

	if err = c.Start(); err != nil {
		return errors.Join(errors.New("could not start http server"), err)
	}

	return nil
}

func (c *Controller) NewEcho(err error) (*echo.Echo, error) {
	echoServer := echo.New()

	echoHandler := echoServer
	wrappedGrpc := grpcweb.WrapServer(c.gRPCServer)
	if c.enablePprof {
		echopprof.Wrap(echoHandler)
	}

	//override server handler to intercept grpc-web requests (content-type: application/grpc-web)
	echoServer.Server = &http.Server{
		Addr: net.JoinHostPort(c.bindHost, strconv.Itoa(c.webAppPort)),
		Handler: http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			resp.Header().Set("Access-Control-Allow-Headers", "*")
			resp.Header().Set("Access-Control-Allow-Origin", "*")

			if wrappedGrpc.IsGrpcWebRequest(req) {
				wrappedGrpc.ServeHTTP(resp, req)
				return
			}

			echoHandler.ServeHTTP(resp, req)
		}),
	}

	if c.assets != nil {
		echoServer.Use(middleware.StaticWithConfig(middleware.StaticConfig{
			Filesystem: c.assets,
			HTML5:      true,
		}))
	}

	echoServer.HideBanner = true
	return echoServer, nil
}

func (c *Controller) Start() (err error) {
	if c.httpTomb != nil && c.httpTomb.Alive() {
		return errors.New("http already started")
	}

	c.echo, err = c.NewEcho(err)
	if err != nil {
		return err
	}

	c.httpTomb = &tomb.Tomb{}
	c.httpTomb.Go(func() error {

		// The REAL address, not a hardcoded "[::]" claim.
		fmt.Printf("⇨ http server started on %s\n", c.echo.Server.Addr)
		if err := c.echo.Server.ListenAndServe(); err != http.ErrServerClosed {
			fmt.Println("(http): server halted forcefully")
			return err
		}

		fmt.Println("(http): server halted gracefully")
		return nil
	})

	c.httpTomb.Go(func() error {
		<-c.httpTomb.Dying()
		return shutdownEcho(c.echo)
	})

	return nil
}

func (c *Controller) Stop() (err error) {
	if c.httpTomb == nil || !c.httpTomb.Alive() {
		return errors.New("http is not started")
	}

	c.httpTomb.Kill(nil)
	if err := c.httpTomb.Wait(); err != nil {
		return err
	}
	return nil
}

func (c *Controller) Quit() {
	if err := c.Stop(); err != nil {
		fmt.Printf("error while exiting http controller. %s\n", err.Error())
	}
}

// echoShutdowner is the part of *echo.Echo the shutdown path uses.
type echoShutdowner interface {
	Shutdown(ctx context.Context) error
	Close() error
}

// shutdownEcho stops the web UI gracefully, then forcibly, and can never take
// the process down with it. W17 fork modification (MAP-11).
//
// Two faults, one function:
//
//   - Shutdown(nil). net/http's Server.Shutdown selects on ctx.Done(), and
//     calling Done() on a nil context is a nil-interface dereference -- so any
//     shutdown with a non-idle connection open panicked. It never took the
//     normal exit path down (httpCtl.Quit is deferred FIRST in main and so runs
//     LAST, after everything else has already shut down), but on the Ctrl-C
//     path it turned a clean exit into a goroutine dump, and this runs inside a
//     tomb goroutine, where a panic is a PROCESS fault. A real context with a
//     deadline replaces it, and a deadline now forces the connections closed
//     instead of hanging.
//
//   - The recover. Whatever else the HTTP stack may do on the way out, the
//     process must not die of it: at this point the ground station has already
//     been told the mapper is stopping, and a crash here would surface to the
//     operator as the drive program having failed.
//
// What it DOES report (review finding N3): a genuine shutdown failure. The
// first cut wrote `if err := server.Shutdown(ctx)`, which SHADOWED the named
// return, so this function could only ever return nil -- a stuck client that
// ran the deadline out reached nobody: not the tomb, not Stop(), not the
// StopHTTP RPC, not the operator. At base that error propagated. A recovered
// PANIC still yields nil, deliberately: it has already been printed, the
// process is on its way out, and turning it into a tomb error would be the
// process fault this function exists to prevent, one indirection later.
func shutdownEcho(server echoShutdowner) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("(http): recovered while shutting the web UI down: %v\n", r)
			err = nil
		}
	}()

	if server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err = server.Shutdown(ctx); err != nil {
		fmt.Printf("(http): the web UI did not stop gracefully within %v, closing it: %s\n",
			shutdownTimeout, err.Error())

		if closeErr := server.Close(); closeErr != nil {
			fmt.Printf("(http): forced close reported: %s\n", closeErr.Error())
			err = errors.Join(err, closeErr)
		}

		return err
	}

	return nil
}
