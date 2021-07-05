module github.com/zond/juicemud

go 1.15

replace github.com/zond/gojuice => /home/zond/projects/gojuice

replace github.com/zond/editorview => /home/zond/projects/editorview

replace github.com/zond/sshtcelltty => /home/zond/projects/sshtcelltty

require (
	github.com/gdamore/tcell/v2 v2.3.11
	github.com/gliderlabs/ssh v0.3.3
	github.com/golang/protobuf v1.3.2 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-runewidth v0.0.13 // indirect
	github.com/sergi/go-diff v1.2.0 // indirect
	github.com/tdewolff/parse/v2 v2.5.18
	github.com/timshannon/badgerhold/v2 v2.0.0-20201228162759-17050a01e34c
	github.com/zond/editorview v0.0.0-00010101000000-000000000000
	github.com/zond/gojuice v0.0.0-20210619192054-5ea4ae215e4c
	github.com/zond/sshtcelltty v0.0.0-20210702125920-c5d9a101a5df
	golang.org/x/crypto v0.0.0-20210616213533-5ff15b29337e
	golang.org/x/term v0.0.0-20210220032956-6a3ed077a48d // indirect
	golang.org/x/text v0.3.5 // indirect
)
