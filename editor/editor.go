package editor

import (
	"fmt"
	"math"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type Editor struct {
	Screen tcell.Screen

	// Edited text
	contentBuffer [][]rune
	// Line wrapped edited text
	wrappedBuffer [][]rune
	// wrapped line/rune pointing into line/rune of buf
	wrappedBufferIndex [][][2]*int
	// number of wrappedBuffer lines hidden above screen
	lineOffset int

	cursor struct {
		x int
		y int
	}
}

func (e *Editor) pollKeys() {
	for {
		switch ev := e.Screen.PollEvent().(type) {
		case *tcell.EventResize:
			e.redraw()
			e.setCursor()
			e.Screen.Show()
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyCtrlC:
				e.Screen.Fini()
				return
			case tcell.KeyUp:
				e.moveCursor(up)
			case tcell.KeyDown:
				e.moveCursor(down)
			case tcell.KeyLeft:
				e.moveCursor(left)
			case tcell.KeyRight:
				e.moveCursor(right)
			}
		}
		e.Screen.Show()
	}
}

type direction uint8

const (
	up direction = iota
	left
	down
	right
)

func (d direction) String() string {
	switch d {
	case up:
		return "up"
	case left:
		return "left"
	case down:
		return "down"
	case right:
		return "right"
	}
	return fmt.Sprintf("unknown:%v", int(d))
}

func (e *Editor) moveCursor(d direction) bool {
	defer func() {
		e.setCursor()
		e.Screen.Show()
	}()
	switch d {
	case up:
		if e.canMoveCursor(up) {
			e.cursor.y--
			return true
		}
	case left:
		if e.canMoveCursor(left) {
			e.cursor.x--
			return true
		} else if e.canMoveCursor(up) {
			e.cursor.y--
			e.cursor.x = len(e.wrappedBuffer[e.cursor.y])
			return true
		}
	case down:
		if e.canMoveCursor(down) {
			e.cursor.y++
			return true
		}
	case right:
		if e.canMoveCursor(right) {
			e.cursor.x++
			return true
		} else if e.canMoveCursor(down) {
			e.cursor.y++
			e.cursor.x = 0
			return true
		}
	}
	return false
}

func (e *Editor) limitInt(i *int, minInc, maxExc int) {
	if *i < minInc {
		*i = minInc
	}
	if *i >= maxExc {
		*i = maxExc - 1
	}
}

func (e *Editor) minInt(i ...int) int {
	res := int(math.MaxInt64)
	for _, j := range i {
		if j < res {
			res = j
		}
	}
	return res
}

func (e *Editor) maxInt(i ...int) int {
	res := int(math.MinInt64)
	for _, j := range i {
		if j > res {
			res = j
		}
	}
	return res
}

func (e *Editor) lineWidth(y int) int {
	if y+e.lineOffset < len(e.wrappedBuffer) {
		return len(e.wrappedBuffer[y+e.lineOffset])
	}
	return 0
}

func (e *Editor) setCursor() {
	width, height := e.Screen.Size()
	if width == 0 || height == 0 {
		return
	}
	e.limitInt(&e.cursor.y, 0, e.minInt(height, len(e.wrappedBuffer)-e.lineOffset+1))
	e.limitInt(&e.cursor.x, 0, e.minInt(width, e.lineWidth(e.cursor.y)+1))
	e.Screen.ShowCursor(e.cursor.x, e.cursor.y)
}

func (e *Editor) canMoveCursor(d direction) bool {
	width, height := e.Screen.Size()
	switch d {
	case up:
		return e.cursor.y > 0
	case left:
		return e.cursor.x > 0
	case down:
		return e.cursor.y+1 < height && e.cursor.y+1 < len(e.wrappedBuffer)-e.lineOffset
	case right:
		return e.cursor.x+1 < width && e.cursor.x+2 < e.lineWidth(e.cursor.y)
	}
	return false
}

func (e *Editor) redraw() {
	e.wrappedBuffer = nil
	e.wrappedBufferIndex = nil

	// No screen makes it impossible to index.
	width, height := e.Screen.Size()
	if width == 0 || height == 0 {
		return
	}

	for contentLineIdxIter, contentLine := range e.contentBuffer {
		contentLineIdx := contentLineIdxIter
		e.wrappedBuffer = append(e.wrappedBuffer, nil)
		e.wrappedBufferIndex = append(e.wrappedBufferIndex, nil)
		for contentRuneIdxIter, contentRune := range contentLine {
			contentRuneIdx := contentRuneIdxIter
			e.wrappedBuffer[len(e.wrappedBuffer)-1] = append(e.wrappedBuffer[len(e.wrappedBuffer)-1], contentRune)
			e.wrappedBufferIndex[len(e.wrappedBufferIndex)-1] = append(e.wrappedBufferIndex[len(e.wrappedBufferIndex)-1], [2]*int{&contentRuneIdx, &contentLineIdx})
			if len(e.wrappedBuffer[len(e.wrappedBuffer)-1]) > width-1 {
				e.wrappedBuffer = append(e.wrappedBuffer, nil)
				e.wrappedBufferIndex = append(e.wrappedBufferIndex, nil)
			}
		}
		e.wrappedBuffer = append(e.wrappedBuffer, nil)
		e.wrappedBufferIndex = append(e.wrappedBufferIndex, nil)
	}

	for wrappedLineIdx, wrappedLine := range e.wrappedBuffer {
		for wrappedRuneIdx, wrappedRune := range wrappedLine {
			e.Screen.SetContent(wrappedRuneIdx, wrappedLineIdx, wrappedRune, nil, tcell.StyleDefault)
		}
		for x := len(e.wrappedBuffer[wrappedLineIdx]); x < width; x++ {
			e.Screen.SetContent(x, wrappedLineIdx, rune(' '), nil, tcell.StyleDefault)
		}
		if wrappedLineIdx+1 > height {
			break
		}
	}
	for y := len(e.wrappedBuffer); y < height; y++ {
		for x := 0; x < width; x++ {
			e.Screen.SetContent(x, y, rune(' '), nil, tcell.StyleDefault)
		}
	}
}

func (e *Editor) Edit(s string) (string, error) {
	e.contentBuffer = nil
	for _, line := range strings.Split(s, "\n") {
		e.contentBuffer = append(e.contentBuffer, []rune(line))
	}
	e.redraw()
	e.setCursor()
	e.Screen.Show()
	e.pollKeys()
	return "", nil
}
