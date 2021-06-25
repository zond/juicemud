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
	editView := NewEditView().
		SetDynamicColors(true).
		SetRegions(true).
		SetChangedFunc(func() {
			app.Draw()
		})
	editView.SetBackgroundColor(tcell.Color(0xffffff))
	editView.SetTextColor(tcell.Color(0x000000))
	//editView.SetText(tview.Escape(s))
	editView.SetText(s)

	screen, err := tcell.NewTerminfoScreenFromTty(&tty.SSHTTY{Sess: e.Sess})
	if err != nil {
		return "", err
	}
	if err := screen.Init(); err != nil {
		return "", err
	}
	app.SetScreen(screen)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyCtrlC:
			app.Stop()
			return nil
		}
		return event
	})

	if err := app.SetRoot(editView, true).SetFocus(editView).Run(); err != nil {
		return "", err
	}

	return "", nil
}
