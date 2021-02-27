package reloadable

import (
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"log"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

var (
	b1, b2, b3                      widget.Clickable
	b1Clicked, b2Clicked, b3Clicked bool
	red                             = color.NRGBA{R: 255, A: 255}
)

func Layout(gtx C, th *material.Theme) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		if b1.Clicked() {
			b1Clicked = !b1Clicked
		}
		if b2.Clicked() {
			b2Clicked = !b2Clicked
		}
		if b3.Clicked() {
			b3Clicked = !b3Clicked
		}
		op.InvalidateOp{}.Add(gtx.Ops)
		return layout.Flex{
			Axis:      layout.Vertical,
			Spacing:   layout.SpaceAround,
			Alignment: layout.Baseline,
		}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				btn := material.Button(th, &b1, "Clickme 1")
				if b1Clicked {
					btn.Background = red
				}
				btn.Inset = layout.UniformInset(unit.Dp(30))
				return btn.Layout(gtx)
			}),
			layout.Flexed(.25, func(gtx C) D {
				btn := material.Button(th, &b2, "Clickme 2")
				if b2Clicked {
					btn.Background = red
				}
				return btn.Layout(gtx)
			}),
			layout.Flexed(.75, func(gtx C) D {
				btn := material.Button(th, &b3, "Clickme 3")
				if b3Clicked {
					btn.Background = red
				}
				return btn.Layout(gtx)
			}),
		)
	})
}
