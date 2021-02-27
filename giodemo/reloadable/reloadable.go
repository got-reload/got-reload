package reloadable

import (
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

var (
	b1, b2, b3 widget.Clickable
)

func Layout(gtx C, th *material.Theme) D {
	// ensure that we invalidate every frame so that
	// changes are immediately visible on reload.
	op.InvalidateOp{}.Add(gtx.Ops)

	/*
			    Try changing this inset, or redefine it as a literal with different
		        values for each side:

				sharedInset := layout.Inset{
					Left:   unit.Dp(4),
					Right:  unit.Dp(8),
					Top:    unit.Dp(1),
					Bottom: unit.Dp(13),
				}
	*/
	sharedInset := layout.UniformInset(unit.Dp(8))
	return layout.Center.Layout(gtx, func(gtx C) D {
		return layout.Flex{
			/*
				Try changing these properties!
			*/
			Axis:      layout.Vertical,
			Spacing:   layout.SpaceAround,
			Alignment: layout.Baseline,
		}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return sharedInset.Layout(gtx, func(gtx C) D {
					btn := material.Button(th, &b1, "Clickme 1")
					/*
						Play with the inset dimensions!
					*/
					btn.Inset = layout.UniformInset(unit.Dp(30))
					return btn.Layout(gtx)
				})
			}),
			/*
				Play with the first parameter here!
				Alternatively, make this a layout.Rigid
			*/
			layout.Flexed(.25, func(gtx C) D {
				return sharedInset.Layout(gtx, func(gtx C) D {
					btn := material.Button(th, &b2, "Clickme 2")
					return btn.Layout(gtx)
				})
			}),
			layout.Flexed(.75, func(gtx C) D {
				return sharedInset.Layout(gtx, func(gtx C) D {
					btn := material.Button(th, &b3, "Clickme 3")
					return btn.Layout(gtx)
				})
			}),
		)
	})
}
