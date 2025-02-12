package lang

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

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

func Card(count int, s string) string {
	if count == 0 {
		return fmt.Sprintf("no %s", Plural(s))
	} else if count == 1 {
		return Indef(Singular(s))
	} else if count == 2 {
		return fmt.Sprintf("two %s", Plural(s))
	} else if count == 3 {
		return fmt.Sprintf("three %s", Plural(s))
	}
	return fmt.Sprintf("%v %s", count, Plural(s))
}

type Tense int

const (
	None Tense = iota
	Present
	Past
)

type Enumerator struct {
	Pattern   string
	Separator string
	Operator  string
	Tense     Tense
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
	switch e.Tense {
	case Present:
		if len(elements) > 1 {
			fmt.Fprintf(res, " are")
		} else if len(elements) > 0 {
			fmt.Fprintf(res, " is")
		}
	case Past:
		fmt.Fprintf(res, " were")
	}
	return res.String()
}

func Indef(s string) string {
	return fmt.Sprintf("%s %s", Article(s), s)
}

// Article returns the indefinite article (a or an) for a given English word.
// The returned string is always "a" or "an".
func Article(word string) string {
	// Handle numbers in digit form.
	// These need to be checked early due to the methods used in some cases
	// below.
	if strings.HasPrefix(word, "8") {
		// Any number starting with an '8' uses 'an'.
		return "an"
	}
	if strings.HasPrefix(word, "11") || strings.HasPrefix(word, "18") {
		// First strip off any decimals and remove spaces or commas.
		// Then if the number of digits modulo 3 is 1 we have a match.
		re := regexp.MustCompile(`^([,.\s]+)`)
		word = re.ReplaceAllLiteralString(word, "")
		if len(word)%3 == 1 {
			return "an"
		}
	}

	// Handle ordinal forms.
	re := regexp.MustCompile(`(?i)^(?:[bcdgjkpqtuvwyz]-?th)`)
	if re.MatchString(word) {
		return "a"
	}
	re = regexp.MustCompile(`(?i)^(?:[aefhilmnorsx]-?th)`)
	if re.MatchString(word) {
		return "an"
	}

	// Handle special cases.
	re = regexp.MustCompile(`(?i)^(?:euler|hour|heir|honest|hono)`)
	if re.MatchString(word) {
		return "an"
	}
	re = regexp.MustCompile(`(?i)^[aefhilmnorsx]$`)
	if re.MatchString(word) {
		return "an"
	}
	re = regexp.MustCompile(`(?i)^[bcdgjkpqtuvwyz]$`)
	if re.MatchString(word) {
		return "a"
	}

	// Handle abbreviations.
	// This pattern matches strings of capitals starting with a "vowel-sound"
	// consonant, followed by another consonant, and which are not likely to
	// be real words.
	// NOTE(akupila): Not sure what words this would match in practice, regex
	// is from PERL implementation.
	re = regexp.MustCompile(`^` +
		`(` +
		`FJO|[HLMNS]Y.|RY[EO]|SQU` +
		`|` +
		`(F[LR]?|[HL]|MN?|N|RH?|S[CHKLMNPTVW]?|X(YL)?)` +
		`[AEIOU]` +
		`)` +
		`[FHLMNRSX][A-Z]`)
	if re.MatchString(word) {
		return "an"
	}

	re = regexp.MustCompile(`(?i)^[aefhilmnorsx][.-]`)
	if re.MatchString(word) {
		return "an"
	}
	re = regexp.MustCompile(`(?i)^[a-z][.-]`)
	if re.MatchString(word) {
		return "a"
	}

	// Handle consonants.
	// The way this is written it will match any digit, as well as non-vowels;
	// this is necessary for later matching of some special cases. Digit
	// recognition needs to be above this. The rule is: case-insensitively
	// match any string that starts with a letter not in [aeiouy].
	re = regexp.MustCompile(`(?i)^[^aeiouy]`)
	if re.MatchString(word) {
		return "a"
	}

	// Handle special vowel-forms.
	re = regexp.MustCompile(`(?i)^e[uw]`)
	if re.MatchString(word) {
		return "a"
	}
	re = regexp.MustCompile(`(?i)^onc?e\b`)
	if re.MatchString(word) {
		return "a"
	}
	re = regexp.MustCompile(`(?i)^uni([^nmd]|mo)`)
	if re.MatchString(word) {
		return "a"
	}
	re = regexp.MustCompile(`(?i)^ut[th]`)
	if re.MatchString(word) {
		return "an"
	}
	re = regexp.MustCompile(`(?i)^u[bcfhjkqrst][aeiou]`)
	if re.MatchString(word) {
		return "a"
	}

	// Handle special capitals.
	re = regexp.MustCompile(`^U[NKR][AIEO]?`)
	if re.MatchString(word) {
		return "a"
	}

	// Handle vowels.
	re = regexp.MustCompile(`(?i)^[aeiou]`)
	if re.MatchString(word) {
		return "an"
	}

	// Handle y with "i.." sound.
	// The pattern encodes the beginnings of all English words beginning with
	// 'y' followed by a consonant. Any other y-consonant prefix therefore
	// implies an abbreviation.
	re = regexp.MustCompile(`(?i)^y(b[lor]|cl[ea]|fere|gg|p[ios]|rou|tt)`)
	if re.MatchString(word) {
		return "an"
	}

	// Otherwise, guess "a".
	return "a"
}
