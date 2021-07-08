package editor

import (
	"github.com/ditashi/jsbeautifier-go/jsbeautifier"
	"github.com/gdamore/tcell/v2"
	"github.com/zond/editorview"
	"github.com/zond/sshtcelltty"
)

func Edit(sess sshtcelltty.InterleavedSSHSession, s string) (string, error) {
	tty := &sshtcelltty.SSHTTY{Sess: sess}
	screen, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		return "", err
	}
	if err := screen.Init(); err != nil {
		return "", err
	}
	ed := editorview.Editor{
		Screen: screen,
	}
	ed.EventFilter = func(untypedEv tcell.Event) []tcell.Event {
		switch ev := untypedEv.(type) {
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyBacktab:
				content := editorview.PlainText(ed.Content())
				pretty, err := jsbeautifier.Beautify(&content, jsbeautifier.DefaultOptions())
				if err == nil {
					ed.SetContent(editorview.Escape(pretty))
				}
			}
		}
		return []tcell.Event{untypedEv}
	}
	return ed.Edit(editorview.Escape(s))
}
