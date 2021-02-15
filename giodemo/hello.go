// SPDX-License-Identifier: Unlicense OR MIT

package main

// A simple Gio program. See https://gioui.org for more information.

import (
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget/material"
	"github.com/got-reload/got-reload/giodemo/reloadable"
	"github.com/got-reload/got-reload/pkg/gotreload"
	"github.com/traefik/yaegi/interp"

	"gioui.org/font/gofont"
)

func main() {
	go func() {
		w := app.NewWindow()
		if err := loop(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func loop(w *app.Window) error {
	th := material.NewTheme(gofont.Collection())
	var ops op.Ops
	for {
		e := <-w.Events()
		switch e := e.(type) {
		case system.DestroyEvent:
			return e.Err
		case system.FrameEvent:
			gtx := layout.NewContext(&ops, e)
			// Ensure that we invalidate every frame so that new versions
			// of the reloadable.Layout function are used immediately.
			op.InvalidateOp{}.Add(gtx.Ops)
			reloadable.Layout(gtx, th)

			e.Frame(gtx.Ops)
		}
	}
}

var Symbols = make(interp.Exports)

func init() {
	gotreload.RegisterAll(Symbols)
}

//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/app
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/app/headless
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/f32
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/font/gofont
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/font/opentype
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/gesture
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/gpu
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/gpu/backend
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/gpu/gl
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/clipboard
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/event
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/key
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/pointer
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/profile
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/router
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/io/system
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/layout
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/op
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/op/clip
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/op/paint
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/text
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/unit
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/widget
//go:generate go run github.com/traefik/yaegi/cmd/yaegi extract gioui.org/widget/material
