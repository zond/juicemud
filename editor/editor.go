package editor

import (
	"github.com/gdamore/tcell/v2"
	"github.com/gliderlabs/ssh"
	"github.com/rivo/tview"
	"github.com/zond/juicemud/tty"
)

type Editor struct {
	Sess ssh.Session
}

func (e *Editor) Edit(s string) (string, error) {
	app := tview.NewApplication()

	tty := &tty.SSHTTY{Sess: e.Sess}
	screen, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		return "", err
	}
	if err := screen.Init(); err != nil {
		return "", err
	}

	app.SetScreen(screen)
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			app.Stop()
		}
		return event
	})

	textView := tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(true).
		SetChangedFunc(func() {
			app.Draw()
		}).
		SetBackgroundColor(tcell.Color(0xffffff))

	if err := app.SetRoot(textView, true).SetFocus(textView).Run(); err != nil {
		return "", err
	}

	return "", nil
}
