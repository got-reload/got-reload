package reloadable

import (
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

var (
	ed     widget.Editor
	offset float64
)

const increment = 0.0001

func Layout(gtx C, th *material.Theme) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		offset += increment
		multiplier := float32(math.Sin(offset))
		final := multiplier * float32(gtx.Constraints.Max.Y/4)
		op.Offset(f32.Pt(0, final)).Add(gtx.Ops)
		op.InvalidateOp{}.Add(gtx.Ops)
		l := material.Editor(th, &ed, "Hello live world!")
		textColor := color.NRGBA{R: 0, G: 200, B: 0, A: 255}
		l.Color = textColor
		return l.Layout(gtx)
	})
}
