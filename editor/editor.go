package editor

import (
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
	ed := editorview.Editor{Screen: screen}
	return ed.Edit(editorview.Escape(s))
}
