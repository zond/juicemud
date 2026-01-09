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

	// Pre-compiled regexes for Article function
	reStripDecimals      = regexp.MustCompile(`^([,.\s]+)`)
	reOrdinalA           = regexp.MustCompile(`(?i)^[bcdgjkpqtuvwyz]-?th`)
	reOrdinalAn          = regexp.MustCompile(`(?i)^[aefhilmnorsx]-?th`)
	reSpecialAn          = regexp.MustCompile(`(?i)^(?:euler|hour|heir|honest|hono)`)
	reSingleLetterAn     = regexp.MustCompile(`(?i)^[aefhilmnorsx]$`)
	reSingleLetterA      = regexp.MustCompile(`(?i)^[bcdgjkpqtuvwyz]$`)
	reAbbreviationAn     = regexp.MustCompile(`^(FJO|[HLMNS]Y.|RY[EO]|SQU|(F[LR]?|[HL]|MN?|N|RH?|S[CHKLMNPTVW]?|X(YL)?)[AEIOU])[FHLMNRSX][A-Z]`)
	reLetterDotDashAn    = regexp.MustCompile(`(?i)^[aefhilmnorsx][.-]`)
	reLetterDotDashA     = regexp.MustCompile(`(?i)^[a-z][.-]`)
	reConsonant          = regexp.MustCompile(`(?i)^[^aeiouy]`)
	reEuwA               = regexp.MustCompile(`(?i)^e[uw]`)
	reOnceA              = regexp.MustCompile(`(?i)^onc?e\b`)
	reUniA               = regexp.MustCompile(`(?i)^uni([^nmd]|mo)`)
	reUtthAn             = regexp.MustCompile(`(?i)^ut[th]`)
	reUConsonantVowelA   = regexp.MustCompile(`(?i)^u[bcfhjkqrst][aeiou]`)
	reSpecialCapitalsA   = regexp.MustCompile(`^U[NKR][AIEO]?`)
	reVowelAn            = regexp.MustCompile(`(?i)^[aeiou]`)
	reYConsonantAn       = regexp.MustCompile(`(?i)^y(b[lor]|cl[ea]|fere|gg|p[ios]|rou|tt)`)
)

func Singular(s string) string {
	return plur.Singular(s)
}

func Plural(s string) string {
	return plur.Plural(s)
}

func Capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[0:1]) + s[1:]
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
			if len(elements) > 2 {
				fmt.Fprintf(res, fmt.Sprintf("%s%%s %%s ", pattern), element, separator, operator)
			} else {
				fmt.Fprintf(res, fmt.Sprintf("%s %%s ", pattern), element, operator)
			}
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
	// These need to be checked early due to the methods used in some cases below.
	if strings.HasPrefix(word, "8") {
		return "an"
	}
	if strings.HasPrefix(word, "11") || strings.HasPrefix(word, "18") {
		// Strip off any decimals and remove spaces or commas.
		// If the number of digits modulo 3 is 1 we have a match.
		stripped := reStripDecimals.ReplaceAllLiteralString(word, "")
		if len(stripped)%3 == 1 {
			return "an"
		}
	}

	// Handle ordinal forms.
	if reOrdinalA.MatchString(word) {
		return "a"
	}
	if reOrdinalAn.MatchString(word) {
		return "an"
	}

	// Handle special cases.
	if reSpecialAn.MatchString(word) {
		return "an"
	}
	if reSingleLetterAn.MatchString(word) {
		return "an"
	}
	if reSingleLetterA.MatchString(word) {
		return "a"
	}

	// Handle abbreviations.
	// This pattern matches strings of capitals starting with a "vowel-sound"
	// consonant, followed by another consonant, and which are not likely to
	// be real words.
	if reAbbreviationAn.MatchString(word) {
		return "an"
	}

	if reLetterDotDashAn.MatchString(word) {
		return "an"
	}
	if reLetterDotDashA.MatchString(word) {
		return "a"
	}

	// Handle consonants.
	// Matches any digit as well as non-vowels; necessary for later matching
	// of special cases. Digit recognition must be above this.
	if reConsonant.MatchString(word) {
		return "a"
	}

	// Handle special vowel-forms.
	if reEuwA.MatchString(word) {
		return "a"
	}
	if reOnceA.MatchString(word) {
		return "a"
	}
	if reUniA.MatchString(word) {
		return "a"
	}
	if reUtthAn.MatchString(word) {
		return "an"
	}
	if reUConsonantVowelA.MatchString(word) {
		return "a"
	}

	// Handle special capitals.
	if reSpecialCapitalsA.MatchString(word) {
		return "a"
	}

	// Handle vowels.
	if reVowelAn.MatchString(word) {
		return "an"
	}

	// Handle y with "i.." sound.
	// The pattern encodes the beginnings of all English words beginning with
	// 'y' followed by a consonant. Any other y-consonant prefix therefore
	// implies an abbreviation.
	if reYConsonantAn.MatchString(word) {
		return "an"
	}

	return "a"
}

// ThirdPersonSingular conjugates a verb for third person singular present tense.
// "slash" → "slashes", "stab" → "stabs", "carry" → "carries"
func ThirdPersonSingular(verb string) string {
	if verb == "" {
		return ""
	}

	// Ends in -s, -x, -z, -ch, -sh: add -es
	if strings.HasSuffix(verb, "s") || strings.HasSuffix(verb, "x") ||
		strings.HasSuffix(verb, "z") || strings.HasSuffix(verb, "ch") ||
		strings.HasSuffix(verb, "sh") {
		return verb + "es"
	}

	// Ends in consonant + y: change to -ies
	if strings.HasSuffix(verb, "y") && len(verb) >= 2 {
		prev := verb[len(verb)-2]
		if !strings.ContainsAny(string(prev), "aeiou") {
			return verb[:len(verb)-1] + "ies"
		}
	}

	// Special cases
	if verb == "go" || verb == "do" {
		return verb + "es"
	}
	if verb == "have" {
		return "has"
	}

	return verb + "s"
}

// Possessive returns the possessive form of a name.
// "John" → "John's", "James" → "James'"
func Possessive(name string) string {
	if name == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(name), "s") {
		return name + "'"
	}
	return name + "'s"
}
