package lang

import (
	"bytes"
	"fmt"
)

const (
	DefaultPattern   = "%s"
	DefaultSeparator = ","
	DefaultOperator  = "and"
)

type Enumerator struct {
	Pattern   string
	Separator string
	Operator  string
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
	return res.String()
}
