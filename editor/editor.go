package editor

import (
	"github.com/gdamore/tcell/v2"
	"github.com/gliderlabs/ssh"
	"github.com/zond/editorview"
	"github.com/zond/juicemud/tty"
)

func Edit(sess ssh.Session, s string) (string, error) {
	tty := &tty.SSHTTY{Sess: sess}
	screen, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		return "", err
	}
	if err := screen.Init(); err != nil {
		return "", err
	}
	ed := editorview.Editor{Screen: screen}
	return ed.Edit(s)
}
