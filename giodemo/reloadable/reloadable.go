package reloadable

import (
	"image/color"

	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/widget/material"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

func Layout(gtx C, th *material.Theme) D {
	return layout.Center.Layout(gtx, func(gtx C) D {
		l := material.H1(th, "Hello live world!")
		textColor := color.NRGBA{R: 100, G: 200, B: 200, A: 255}
		l.Color = textColor
		l.Alignment = text.Middle
		return l.Layout(gtx)
	})
}
