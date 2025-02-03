package lang

import (
	"bytes"
	"fmt"

	"github.com/gertd/go-pluralize"
)

const (
	DefaultPattern   = "%s"
	DefaultSeparator = ","
	DefaultOperator  = "and"
)

var (
	plur = pluralize.NewClient()
)

func Singular(s string) string {
	return plur.Singular(s)
}

func Plural(s string) string {
	return plur.Plural(s)
}

func Declare(count int, s string) string {
	if count == 0 {
		return fmt.Sprintf("no %s", Plural(s))
	} else if count == 1 {
		return fmt.Sprintf("1 %s", Singular(s))
	}
	return fmt.Sprintf("%v %s", count, Plural(s))
}

type Enumerator struct {
	Pattern   string
	Separator string
	Operator  string
	Active    bool
}

func (e Enumerator) Do(elements ...string) string {
	pattern, separator, operator := DefaultPattern, DefaultSeparator, DefaultOperator
	if e.Pattern != "" {
		pattern = e.Pattern
	}
	if e.Separator != "" {
		separator = e.Separator
	}
	if e.Operator != "" {
		operator = e.Operator
	}
	res := &bytes.Buffer{}
	for idx, element := range elements {
		if idx+2 < len(elements) {
			fmt.Fprintf(res, fmt.Sprintf("%s%%s ", pattern), element, separator)
		} else if idx+1 < len(elements) {
			fmt.Fprintf(res, fmt.Sprintf("%s%%s %%s ", pattern), element, separator, operator)
		} else {
			fmt.Fprintf(res, pattern, element)
		}
	}
	if e.Active {
		if len(elements) > 1 {
			fmt.Fprintf(res, " are")
		} else if len(elements) > 0 {
			fmt.Fprintf(res, " is")
		}
	}
	return res.String()
}
