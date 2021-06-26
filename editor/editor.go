package editor

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

type Editor struct {
	Screen      tcell.Screen
	buf         [][]rune
	bufIndex    [][][2]*int
	lineLengths []int
	lineOffset  int
	cursor      struct {
		x int
		y int
	}
}

func (e *Editor) pollKeys() {
	for {
		switch ev := e.Screen.PollEvent().(type) {
		case *tcell.EventResize:
			e.setContent()
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyCtrlC:
				e.Screen.Fini()
				return
			case tcell.KeyUp:
				e.cursor.y--
				e.setCursor()
			case tcell.KeyDown:
				e.cursor.y++
				e.setCursor()
			case tcell.KeyLeft:
				e.cursor.x--
				e.setCursor()
			case tcell.KeyRight:
				e.cursor.x++
				e.setCursor()
			}
		}
		e.Screen.Show()
	}
}

func (e *Editor) limitInt(i *int, minInc, maxExc int) {
	if *i < minInc {
		*i = minInc
	}
	if *i >= maxExc {
		*i = maxExc - 1
	}
}

func (e *Editor) minInt(i, j int) int {
	if i < j {
		return i
	}
	return j
}

func (e *Editor) maxInt(i, j int) int {
	if i > j {
		return i
	}
	return j
}

func (e *Editor) setCursor() {
	width, height := e.Screen.Size()
	if width == 0 || height == 0 {
		return
	}
	e.limitInt(&e.cursor.y, 0, height)
	e.limitInt(&e.cursor.x, 0, e.minInt(e.lineLengths[e.cursor.y]+1, width))
	e.Screen.ShowCursor(e.cursor.x, e.cursor.y)
}

func (e *Editor) setContent() {
	width, height := e.Screen.Size()
	if width == 0 || height == 0 {
		return
	}

	x, y := 0, 0
	eol := func() {
		e.lineLengths[y] = x
		for ; x < width; x++ {
			e.Screen.SetContent(x, y, rune(' '), nil, tcell.StyleDefault)
		}
		x = 0
		y++
	}

	if len(e.bufIndex) != height {
		e.bufIndex = make([][][2]*int, height)
	}
	if len(e.lineLengths) != height {
		e.lineLengths = make([]int, height)
	}

	for lineIdxIter, line := range e.buf[e.lineOffset:] {
		lineIdx := lineIdxIter
		for runeIdxIter, run := range line {
			runeIdx := runeIdxIter
			e.Screen.SetContent(x, y, run, nil, tcell.StyleDefault)
			if len(e.bufIndex[y]) != width {
				e.bufIndex[y] = make([][2]*int, width)
			}
			e.bufIndex[y][x] = [2]*int{&lineIdx, &runeIdx}
			if x < width-1 {
				x++
			} else {
				eol()
			}
			if y > height-1 {
				break
			}
		}
		if y > height-1 {
			break
		}
		eol()
		if y > height-1 {
			break
		}
	}
	for ; y < height; y++ {
		for x = 0; x < width; x++ {
			e.Screen.SetContent(x, y, rune(' '), nil, tcell.StyleDefault)
		}
	}
}

func (e *Editor) Edit(s string) (string, error) {
	e.buf = nil
	for _, line := range strings.Split(s, "\n") {
		e.buf = append(e.buf, []rune(line))
	}
	e.setContent()
	e.setCursor()
	e.Screen.Show()
	e.pollKeys()
	return "", nil
}
