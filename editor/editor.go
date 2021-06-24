package editor

import (
	"fmt"

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
		if width > 0 && height > 0 {
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
	e.editView.SetText(fmt.Sprintf("[#000000]%s", s))

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
		width, height := e.screen.Size()
		scrollY, scrollX := e.editView.GetScrollOffset()
		switch event.Key() {
		case tcell.KeyCtrlC:
			e.app.Stop()
		case tcell.KeyDown:
			if e.cursor.y+1 < height {
				e.cursor.y++
				e.updateCursor()
			} else if e.cursor.y+scrollY+1 < e.editView.Lines() {
				scrollY++
				e.editView.ScrollTo(scrollY, scrollX)
				e.updateCursor()
			}
		case tcell.KeyUp:
			if e.cursor.y > 0 {
				e.cursor.y--
				e.updateCursor()
			} else if scrollY > 0 {
				scrollY--
				e.editView.ScrollTo(scrollY, scrollX)
				e.updateCursor()
			}
		case tcell.KeyLeft:
			if e.cursor.x > 0 {
				e.cursor.x--
				e.updateCursor()
			} else if e.cursor.y > 0 {
				e.cursor.y--
				e.cursor.x = e.practicalWidth(e.cursor.y)
				if e.cursor.x+2 > width {
					e.cursor.x = width - 1
				}
				e.updateCursor()
			}
		case tcell.KeyRight:
			if pw := e.practicalWidth(e.cursor.y); e.cursor.x < pw && e.cursor.x+1 < width {
				e.cursor.x++
				e.updateCursor()
			} else if e.cursor.y+1 < height {
				e.cursor.y++
				e.cursor.x = 0
				e.updateCursor()
			}
		}
		return nil
	})

	if err := e.app.SetRoot(e.editView, true).SetFocus(e.editView).Run(); err != nil {
		return "", err
	}

	return "", nil
}
