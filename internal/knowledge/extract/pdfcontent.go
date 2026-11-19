package extract

import (
	"fmt"

	"github.com/ledongthuc/pdf"
)

// The library's Page.Content() appends a fake "\n" glyph after every TJ operator, decoded through
// the current font: with subset fonts it turns into a random letter at the end of each line.
// This is a minimal re-implementation of the text-positioning operators on top of the library's
// content-stream interpreter.

const (
	glyphSpaceUnits = 1000 // glyph widths and TJ offsets are in thousandths of text space
	percent         = 100
	matrixArgs      = 6
	spaceCode       = ' '
)

type matrix [3][3]float64

var identity = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (m matrix) mul(n matrix) matrix {
	var r matrix

	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				r[i][j] += m[i][k] * n[k][j]
			}
		}
	}

	return r
}

func translate(tx, ty float64) matrix {
	return matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
}

// glyph is one decoded character with its position in user space.
type glyph struct {
	x, y     float64
	width    float64
	fontSize float64
	text     string
}

type graphicsState struct {
	ctm       matrix
	tm        matrix // text matrix
	tlm       matrix // text line matrix
	font      pdf.Font
	enc       pdf.TextEncoding
	fontSize  float64
	charSpace float64
	wordSpace float64
	scale     float64 // horizontal scaling, 1 = 100%
	leading   float64
	rise      float64
}

type contentReader struct {
	page      pdf.Page
	state     graphicsState
	stack     []graphicsState
	glyphs    []glyph
	maxGlyphs int
	overflow  bool // more than maxGlyphs glyphs: the rest are dropped and the page is rejected
}

type operator func(r *contentReader, args []pdf.Value)

var operators = map[string]operator{
	"q":  func(r *contentReader, _ []pdf.Value) { r.stack = append(r.stack, r.state) },
	"Q":  (*contentReader).restore,
	"cm": (*contentReader).concat,
	"BT": func(r *contentReader, _ []pdf.Value) { r.state.tm, r.state.tlm = identity, identity },
	"Td": (*contentReader).moveText,
	"TD": func(r *contentReader, args []pdf.Value) {
		if len(args) == 2 {
			r.state.leading = -args[1].Float64()
		}

		r.moveText(args)
	},
	"Tm": (*contentReader).setTextMatrix,
	"T*": func(r *contentReader, _ []pdf.Value) { r.nextLine() },
	"Tf": (*contentReader).setFont,
	"Tc": func(r *contentReader, args []pdf.Value) { setFloat(&r.state.charSpace, args) },
	"Tw": func(r *contentReader, args []pdf.Value) { setFloat(&r.state.wordSpace, args) },
	"TL": func(r *contentReader, args []pdf.Value) { setFloat(&r.state.leading, args) },
	"Ts": func(r *contentReader, args []pdf.Value) { setFloat(&r.state.rise, args) },
	"Tz": func(r *contentReader, args []pdf.Value) {
		if setFloat(&r.state.scale, args) {
			r.state.scale /= percent
		}
	},
	"Tj": (*contentReader).showArgs,
	"'": func(r *contentReader, args []pdf.Value) {
		r.nextLine()
		r.showArgs(args)
	},
	"\"": func(r *contentReader, args []pdf.Value) {
		if len(args) != 3 {
			return
		}

		r.state.wordSpace, r.state.charSpace = args[0].Float64(), args[1].Float64()
		r.nextLine()
		r.showArgs(args[2:])
	},
	"TJ": (*contentReader).showPositioned,
}

// readPageGlyphs returns the page's characters in content-stream order; more than maxGlyphs is an error.
func readPageGlyphs(page pdf.Page, maxGlyphs int) ([]glyph, error) {
	contents := page.V.Key("Contents")
	if contents.IsNull() {
		return nil, nil
	}

	r := &contentReader{
		page:      page,
		state:     graphicsState{ctm: identity, tm: identity, tlm: identity, scale: 1},
		maxGlyphs: maxGlyphs,
	}

	pdf.Interpret(contents, func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}

		if handle, ok := operators[op]; ok && !r.overflow {
			handle(r, args)
		}
	})

	if r.overflow {
		return nil, fmt.Errorf("page has more than %d glyphs: %w: %w", maxGlyphs, ErrInvalidDocument, errPDFLimit)
	}

	return r.glyphs, nil
}

func setFloat(dst *float64, args []pdf.Value) bool {
	if len(args) != 1 {
		return false
	}

	*dst = args[0].Float64()

	return true
}

func matrixFromArgs(args []pdf.Value) (matrix, bool) {
	if len(args) != matrixArgs {
		return matrix{}, false
	}

	var m matrix
	for i, a := range args {
		m[i/2][i%2] = a.Float64()
	}

	m[2][2] = 1

	return m, true
}

func (r *contentReader) restore(_ []pdf.Value) {
	if n := len(r.stack); n > 0 {
		r.state = r.stack[n-1]
		r.stack = r.stack[:n-1]
	}
}

func (r *contentReader) concat(args []pdf.Value) {
	if m, ok := matrixFromArgs(args); ok {
		r.state.ctm = m.mul(r.state.ctm)
	}
}

func (r *contentReader) setTextMatrix(args []pdf.Value) {
	if m, ok := matrixFromArgs(args); ok {
		r.state.tm, r.state.tlm = m, m
	}
}

func (r *contentReader) moveText(args []pdf.Value) {
	if len(args) != 2 {
		return
	}

	r.state.tlm = translate(args[0].Float64(), args[1].Float64()).mul(r.state.tlm)
	r.state.tm = r.state.tlm
}

func (r *contentReader) nextLine() {
	r.state.tlm = translate(0, -r.state.leading).mul(r.state.tlm)
	r.state.tm = r.state.tlm
}

func (r *contentReader) setFont(args []pdf.Value) {
	if len(args) != 2 {
		return
	}

	r.state.font = r.page.Font(args[0].Name())
	r.state.enc = r.state.font.Encoder()
	r.state.fontSize = args[1].Float64()
}

func (r *contentReader) showArgs(args []pdf.Value) {
	if len(args) == 1 {
		r.show(args[0].RawString())
	}
}

// showPositioned handles TJ: strings interleaved with offsets in thousandths of the font size.
func (r *contentReader) showPositioned(args []pdf.Value) {
	if len(args) != 1 {
		return
	}

	arr := args[0]
	for i := range arr.Len() {
		item := arr.Index(i)
		if item.Kind() == pdf.String {
			r.show(item.RawString())

			continue
		}

		tx := -item.Float64() / glyphSpaceUnits * r.state.fontSize * r.state.scale
		r.state.tm = translate(tx, 0).mul(r.state.tm)
	}
}

// show decodes a string and advances the text matrix glyph by glyph. Widths are looked up by byte,
// as the library does: for two-byte (CID) fonts positions are approximate.
func (r *contentReader) show(raw string) {
	if r.state.enc == nil {
		return
	}

	s := &r.state
	decoded := []rune(s.enc.Decode(raw))

	for i, ch := range decoded {
		if len(r.glyphs) >= r.maxGlyphs {
			r.overflow = true

			return
		}

		var w0 float64
		if i < len(raw) {
			w0 = s.font.Width(int(raw[i]))
		}

		trm := matrix{{s.fontSize * s.scale, 0, 0}, {0, s.fontSize, 0}, {0, s.rise, 1}}.mul(s.tm).mul(s.ctm)
		r.glyphs = append(r.glyphs, glyph{
			x:        trm[2][0],
			y:        trm[2][1],
			width:    w0 / glyphSpaceUnits * trm[0][0],
			fontSize: trm[0][0],
			text:     string(ch),
		})

		tx := w0/glyphSpaceUnits*s.fontSize + s.charSpace
		if len(raw) == len(decoded) && raw[i] == spaceCode {
			tx += s.wordSpace
		}

		s.tm = translate(tx*s.scale, 0).mul(s.tm)
	}
}
