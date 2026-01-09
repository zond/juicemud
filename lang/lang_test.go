package lang

import (
	"testing"
)

func TestSingular(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"swords", "sword"},
		{"axes", "axe"},
		{"knives", "knife"},
		{"enemies", "enemy"},
		{"children", "child"},
		{"men", "man"},
		{"mice", "mouse"},
		{"sheep", "sheep"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Singular(tt.input); got != tt.expected {
				t.Errorf("Singular(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestPlural(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sword", "swords"},
		{"axe", "axes"},
		{"knife", "knives"},
		{"enemy", "enemies"},
		{"child", "children"},
		{"man", "men"},
		{"mouse", "mice"},
		{"sheep", "sheep"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Plural(tt.input); got != tt.expected {
				t.Errorf("Plural(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCapitalize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sword", "Sword"},
		{"hello world", "Hello world"},
		{"ALREADY", "ALREADY"},
		{"a", "A"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Capitalize(tt.input); got != tt.expected {
				t.Errorf("Capitalize(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCard(t *testing.T) {
	tests := []struct {
		count    int
		word     string
		expected string
	}{
		{0, "sword", "no swords"},
		{1, "sword", "a sword"},
		{1, "axe", "an axe"},
		{2, "sword", "two swords"},
		{3, "sword", "three swords"},
		{4, "sword", "4 swords"},
		{100, "enemy", "100 enemies"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := Card(tt.count, tt.word); got != tt.expected {
				t.Errorf("Card(%d, %q) = %q, want %q", tt.count, tt.word, got, tt.expected)
			}
		})
	}
}

func TestArticle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Vowels
		{"apple", "an"},
		{"orange", "an"},
		{"umbrella", "an"},
		{"elephant", "an"},
		{"igloo", "an"},
		// Consonants
		{"sword", "a"},
		{"battle", "a"},
		{"castle", "a"},
		{"dragon", "a"},
		// Special cases
		{"hour", "an"},
		{"honest", "an"},
		{"heir", "an"},
		{"university", "a"},
		{"unicorn", "a"},
		{"one", "a"},
		{"once", "a"},
		// Numbers
		{"8-legged", "an"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Article(tt.input); got != tt.expected {
				t.Errorf("Article(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIndef(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sword", "a sword"},
		{"axe", "an axe"},
		{"apple", "an apple"},
		{"hour", "an hour"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Indef(tt.input); got != tt.expected {
				t.Errorf("Indef(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestEnumerator(t *testing.T) {
	tests := []struct {
		name     string
		enum     Enumerator
		elements []string
		expected string
	}{
		{
			name:     "single element",
			enum:     Enumerator{},
			elements: []string{"sword"},
			expected: "sword",
		},
		{
			name:     "two elements",
			enum:     Enumerator{},
			elements: []string{"sword", "shield"},
			expected: "sword and shield",
		},
		{
			name:     "three elements",
			enum:     Enumerator{},
			elements: []string{"sword", "shield", "helmet"},
			expected: "sword, shield, and helmet",
		},
		{
			name:     "with or operator",
			enum:     Enumerator{Operator: "or"},
			elements: []string{"sword", "axe"},
			expected: "sword or axe",
		},
		{
			name:     "with pattern",
			enum:     Enumerator{Pattern: "a %s"},
			elements: []string{"sword", "shield"},
			expected: "a sword and a shield",
		},
		{
			name:     "present tense single",
			enum:     Enumerator{Tense: Present},
			elements: []string{"sword"},
			expected: "sword is",
		},
		{
			name:     "present tense multiple",
			enum:     Enumerator{Tense: Present},
			elements: []string{"sword", "shield"},
			expected: "sword and shield are",
		},
		{
			name:     "past tense",
			enum:     Enumerator{Tense: Past},
			elements: []string{"sword", "shield"},
			expected: "sword and shield were",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.enum.Do(tt.elements...); got != tt.expected {
				t.Errorf("Enumerator.Do(%v) = %q, want %q", tt.elements, got, tt.expected)
			}
		})
	}
}

func TestThirdPersonSingular(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Empty
		{"", ""},
		// Regular verbs: add -s
		{"stab", "stabs"},
		{"kick", "kicks"},
		{"hit", "hits"},
		{"cut", "cuts"},
		{"throw", "throws"},
		{"strike", "strikes"},
		{"pierce", "pierces"},
		{"slice", "slices"},
		{"chop", "chops"},
		{"swing", "swings"},
		// Ends in -s, -sh, -ch, -x, -z: add -es
		{"slash", "slashes"},
		{"smash", "smashes"},
		{"crush", "crushes"},
		{"bash", "bashes"},
		{"punch", "punches"},
		{"notch", "notches"},
		{"miss", "misses"},
		{"toss", "tosses"},
		{"fix", "fixes"},
		{"buzz", "buzzes"},
		// Consonant + y: change to -ies
		{"carry", "carries"},
		{"parry", "parries"},
		// Vowel + y: just add -s
		{"spray", "sprays"},
		{"slay", "slays"},
		{"play", "plays"},
		// Special cases
		{"go", "goes"},
		{"do", "does"},
		{"have", "has"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ThirdPersonSingular(tt.input); got != tt.expected {
				t.Errorf("ThirdPersonSingular(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestPossessive(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Empty
		{"", ""},
		// Regular names: add 's
		{"John", "John's"},
		{"Mary", "Mary's"},
		{"Dragon", "Dragon's"},
		{"goblin", "goblin's"},
		// Names ending in s: add just '
		{"James", "James'"},
		{"Charles", "Charles'"},
		{"boss", "boss'"},
		{"Marcus", "Marcus'"},
		// Case insensitive check for s
		{"jess", "jess'"},
		{"JESS", "JESS'"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := Possessive(tt.input); got != tt.expected {
				t.Errorf("Possessive(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
