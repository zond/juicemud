package termio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zond/juicemud/lang"
	"golang.org/x/crypto/ssh/terminal"
)

type TerminalFunc func(*terminal.Terminal) error

func TerminalExecute(term *terminal.Terminal, commands map[string]TerminalFunc) error {
	commandNames := make(sort.StringSlice, 0, len(commands))
	for name := range commands {
		commandNames = append(commandNames, name)
	}
	sort.Sort(commandNames)
	prompt := fmt.Sprintf("%s\n\n", lang.Enumerator{Pattern: "[%s]", Operator: "or"}.Do(commandNames...))
	for {
		fmt.Fprint(term, prompt)
		line, err := term.ReadLine()
		if err != nil {
			return err
		}
		if cmd, found := commands[line]; found {
			if err := cmd(term); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func TerminalSelect(term *terminal.Terminal, prompt string, options []string) (string, error) {
	for {
		fmt.Fprintf(term, "%s [%s]\n\n", prompt, strings.Join(options, "/"))
		line, err := term.ReadLine()
		if err != nil {
			return "", err
		}
		for _, option := range options {
			if strings.ToLower(line) == strings.ToLower(option) {
				return option, nil
			}
		}
	}
}
