package editor

import (
	"github.com/gdamore/tcell/v2"
	"github.com/gliderlabs/ssh"
	"github.com/rivo/tview"
	"github.com/zond/juicemud/tty"
)

type Editor struct {
	Sess ssh.Session

	editView *EditView
	screen   tcell.Screen
	tty      *tty.SSHTTY
	app      *tview.Application
	cursor   struct {
		x int
		y int
	}
}

func (e *Editor) write(r rune) {
	e.editView.WriteAt(r, e.cursor.x, e.cursor.y)
}

func (e *Editor) practicalHeight() int {
	return e.editView.Lines()
}

func (e *Editor) practicalWidth(y int) int {
	width, _, _ := e.tty.WindowSize()
	for width > 0 {
		_, hasByte := e.editView.ByteAt(width-1, y)
		if hasByte {
			return width
		}
		width--
	}
	return width
}

func (e *Editor) updateCursor() {
	if e.screen != nil {
		width, height, _ := e.tty.WindowSize()
		if width > 0 && height > 0 && e.editView.Lines() > 0 {
			if ph := e.practicalHeight(); e.cursor.y+2 > ph {
				e.cursor.y = ph - 1
			}
			if e.cursor.y+2 > height {
				e.cursor.y = height - 1
			}
			if pw := e.practicalWidth(e.cursor.y); e.cursor.x+1 > pw {
				e.cursor.x = pw
			}
			if e.cursor.x+2 > width {
				e.cursor.x = width - 1
			}
			e.screen.ShowCursor(e.cursor.x, e.cursor.y)
		}
	}
}

func (e *Editor) resized() {
	e.updateCursor()
}

func (e *Editor) cursorDown() bool {
	_, height := e.screen.Size()
	scrollY, scrollX := e.editView.GetScrollOffset()
	if ph := e.practicalHeight(); e.cursor.y+1 < ph && e.cursor.y+1 < height {
		e.cursor.y++
		e.updateCursor()
		return true
	} else if e.cursor.y+scrollY+1 < e.editView.Lines() {
		scrollY++
		e.editView.ScrollTo(scrollY, scrollX)
		e.updateCursor()
		return true
	}
	return false
}

func (e *Editor) cursorUp() bool {
	scrollY, scrollX := e.editView.GetScrollOffset()
	if e.cursor.y > 0 {
		e.cursor.y--
		e.updateCursor()
		return true
	} else if scrollY > 0 {
		scrollY--
		e.editView.ScrollTo(scrollY, scrollX)
		e.updateCursor()
		return true
	}
	return false
}

func (e *Editor) cursorRight() bool {
	width, height := e.screen.Size()
	scrollY, scrollX := e.editView.GetScrollOffset()
	if pw := e.practicalWidth(e.cursor.y); e.cursor.x < pw && e.cursor.x+1 < width {
		e.cursor.x++
		e.updateCursor()
		return true
	} else if ph := e.practicalHeight(); e.cursor.y+1 < ph && e.cursor.y+1 < height {
		e.cursor.y++
		e.cursor.x = 0
		e.updateCursor()
		return true
	} else if e.cursor.y+scrollY+1 < e.editView.Lines() {
		scrollY++
		e.editView.ScrollTo(scrollY, scrollX)
		e.updateCursor()
		return true
	}
	return false
}

func (e *Editor) cursorLeft() bool {
	width, _ := e.screen.Size()
	scrollY, scrollX := e.editView.GetScrollOffset()
	if e.cursor.x > 0 {
		e.cursor.x--
		e.updateCursor()
		return true
	} else if e.cursor.y > 0 {
		e.cursor.y--
		e.cursor.x = e.practicalWidth(e.cursor.y)
		if e.cursor.x+2 > width {
			e.cursor.x = width - 1
		}
		e.updateCursor()
		return true
	} else if scrollY > 0 {
		scrollY--
		e.editView.ScrollTo(scrollY, scrollX)
		e.updateCursor()
		return true
	}
	return false
}

func (e *Editor) indentAt(y int) int {
	pw := e.practicalWidth(y)
	for x := 0; x < pw; x++ {
		b, found := e.editView.ByteAt(x, y)
		if !found || spacePattern.MatchString(string([]byte{b})) {
			return x
		}
	}
	return pw
}

func (e *Editor) Edit(s string) (string, error) {
	e.app = tview.NewApplication()
	e.editView = NewEditView().
		SetDynamicColors(true).
		SetRegions(true).
		SetChangedFunc(func() {
			e.app.Draw()
			e.updateCursor()
		})
	e.editView.SetBackgroundColor(tcell.Color(0xffffff))
	e.editView.SetText(tview.Escape(s))

	e.cursor.x = 0
	e.cursor.y = 0

	e.tty = &tty.SSHTTY{Sess: e.Sess, ResizeCallback: e.resized}
	var err error
	if e.screen, err = tcell.NewTerminfoScreenFromTty(e.tty); err != nil {
		return "", err
	}
	if err := e.screen.Init(); err != nil {
		return "", err
	}
	e.app.SetScreen(e.screen)

	e.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyCtrlC:
			e.app.Stop()
		case tcell.KeyDown:
			if event.Modifiers()&tcell.ModCtrl != 0 {
				indent := e.indentAt(e.cursor.y)
				for e.cursorDown() {
					if e.indentAt(e.cursor.y) != indent {
						break
					}
				}
			} else {
				e.cursorDown()
			}
		case tcell.KeyUp:
			if event.Modifiers()&tcell.ModCtrl != 0 {
				indent := e.indentAt(e.cursor.y)
				for e.cursorUp() {
					if e.indentAt(e.cursor.y) != indent {
						break
					}
				}
			} else {
				e.cursorUp()
			}
		case tcell.KeyLeft:
			if event.Modifiers()&tcell.ModCtrl != 0 {
				b, _ := e.editView.ByteAt(e.cursor.x, e.cursor.y)
				isWhite := spacePattern.MatchString(string([]byte{b}))
				for e.cursorLeft() {
					b, _ = e.editView.ByteAt(e.cursor.x, e.cursor.y)
					if isWhite != spacePattern.MatchString(string([]byte{b})) {
						break
					}
				}
			} else {
				e.cursorLeft()
			}
		case tcell.KeyRight:
			if event.Modifiers()&tcell.ModCtrl != 0 {
				b, _ := e.editView.ByteAt(e.cursor.x, e.cursor.y)
				isWhite := spacePattern.MatchString(string([]byte{b}))
				for e.cursorRight() {
					b, _ = e.editView.ByteAt(e.cursor.x, e.cursor.y)
					if isWhite != spacePattern.MatchString(string([]byte{b})) {
						break
					}
				}
			} else {
				e.cursorRight()
			}
		default:
			e.write(event.Rune())
		}
		return nil
	})

	if err := e.app.SetRoot(e.editView, true).SetFocus(e.editView).Run(); err != nil {
		return "", err
	}

	return "", nil
}
