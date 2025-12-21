package game

import (
	"reflect"
	"testing"
)

func TestParseShellTokens(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		n          int
		wantTokens []string
		wantRest   string
	}{
		{
			name:       "empty input",
			input:      "",
			n:          0,
			wantTokens: nil,
			wantRest:   "",
		},
		{
			name:       "single token",
			input:      "hello",
			n:          0,
			wantTokens: []string{"hello"},
			wantRest:   "",
		},
		{
			name:       "multiple tokens",
			input:      "hello world foo",
			n:          0,
			wantTokens: []string{"hello", "world", "foo"},
			wantRest:   "",
		},
		{
			name:       "limit tokens",
			input:      "hello world foo bar",
			n:          2,
			wantTokens: []string{"hello", "world"},
			wantRest:   "foo bar",
		},
		{
			name:       "single quotes",
			input:      "'hello world'",
			n:          0,
			wantTokens: []string{"hello world"},
			wantRest:   "",
		},
		{
			name:       "double quotes",
			input:      `"hello world"`,
			n:          0,
			wantTokens: []string{"hello world"},
			wantRest:   "",
		},
		{
			name:       "mixed quotes",
			input:      `'hello' "world"`,
			n:          0,
			wantTokens: []string{"hello", "world"},
			wantRest:   "",
		},
		{
			name:       "backslash escape outside quotes",
			input:      `hello\ world`,
			n:          0,
			wantTokens: []string{"hello world"},
			wantRest:   "",
		},
		{
			name:       "backslash escape inside double quotes",
			input:      `"hello\"world"`,
			n:          0,
			wantTokens: []string{`hello"world`},
			wantRest:   "",
		},
		{
			name:       "limit with quotes preserves rest",
			input:      `#objectID Spawn.Container "some value"`,
			n:          2,
			wantTokens: []string{"#objectID", "Spawn.Container"},
			wantRest:   `"some value"`,
		},
		{
			name:       "whitespace handling",
			input:      "  hello   world  ",
			n:          0,
			wantTokens: []string{"hello", "world"},
			wantRest:   "",
		},
		{
			name:       "tabs as whitespace",
			input:      "hello\tworld",
			n:          0,
			wantTokens: []string{"hello", "world"},
			wantRest:   "",
		},
		{
			name:       "limit one returns rest",
			input:      "first second third",
			n:          1,
			wantTokens: []string{"first"},
			wantRest:   "second third",
		},
		{
			name:       "adjacent quotes",
			input:      `"hello"'world'`,
			n:          0,
			wantTokens: []string{"helloworld"},
			wantRest:   "",
		},
		{
			name:       "quote in middle of token",
			input:      `foo"bar baz"qux`,
			n:          0,
			wantTokens: []string{"foobar bazqux"},
			wantRest:   "",
		},
		{
			name:       "unclosed single quote",
			input:      "'hello",
			n:          0,
			wantTokens: []string{"hello"},
			wantRest:   "",
		},
		{
			name:       "unclosed double quote",
			input:      `"hello`,
			n:          0,
			wantTokens: []string{"hello"},
			wantRest:   "",
		},
		{
			name:       "trailing backslash",
			input:      `hello\`,
			n:          0,
			wantTokens: []string{"hello"},
			wantRest:   "",
		},
		{
			name:       "setstate typical usage",
			input:      "/setstate Spawn.Container genesis",
			n:          2,
			wantTokens: []string{"/setstate", "Spawn.Container"},
			wantRest:   "genesis",
		},
		{
			name:       "json value preserved",
			input:      `/setstate Foo {"nested": "value"}`,
			n:          2,
			wantTokens: []string{"/setstate", "Foo"},
			wantRest:   `{"nested": "value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTokens, gotRest := parseShellTokens(tt.input, tt.n)
			if !reflect.DeepEqual(gotTokens, tt.wantTokens) {
				t.Errorf("parseShellTokens(%q, %d) tokens = %v, want %v", tt.input, tt.n, gotTokens, tt.wantTokens)
			}
			if gotRest != tt.wantRest {
				t.Errorf("parseShellTokens(%q, %d) rest = %q, want %q", tt.input, tt.n, gotRest, tt.wantRest)
			}
		})
	}
}

func TestNavigatePath(t *testing.T) {
	data := map[string]any{
		"simple": "value",
		"nested": map[string]any{
			"level1": map[string]any{
				"level2": "deep value",
			},
		},
		"array": []any{"a", "b", "c"},
		"number": 42,
	}

	tests := []struct {
		name string
		path string
		want any
	}{
		{
			name: "empty path returns whole map",
			path: "",
			want: data,
		},
		{
			name: "dot returns whole map",
			path: ".",
			want: data,
		},
		{
			name: "simple key",
			path: "simple",
			want: "value",
		},
		{
			name: "nested path",
			path: "nested.level1.level2",
			want: "deep value",
		},
		{
			name: "nested map",
			path: "nested.level1",
			want: map[string]any{"level2": "deep value"},
		},
		{
			name: "array value",
			path: "array",
			want: []any{"a", "b", "c"},
		},
		{
			name: "number value",
			path: "number",
			want: 42,
		},
		{
			name: "non-existent key",
			path: "missing",
			want: nil,
		},
		{
			name: "non-existent nested key",
			path: "nested.missing.key",
			want: nil,
		},
		{
			name: "path through non-map",
			path: "simple.key",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := navigatePath(data, tt.path)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("navigatePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSetPath(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]any
		path    string
		value   any
		wantErr bool
		wantMap map[string]any
	}{
		{
			name:    "set simple key",
			initial: map[string]any{},
			path:    "key",
			value:   "value",
			wantMap: map[string]any{"key": "value"},
		},
		{
			name:    "set nested key creates intermediates",
			initial: map[string]any{},
			path:    "a.b.c",
			value:   "deep",
			wantMap: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": "deep",
					},
				},
			},
		},
		{
			name:    "update existing key",
			initial: map[string]any{"key": "old"},
			path:    "key",
			value:   "new",
			wantMap: map[string]any{"key": "new"},
		},
		{
			name: "update nested key",
			initial: map[string]any{
				"parent": map[string]any{
					"child": "old",
				},
			},
			path:  "parent.child",
			value: "new",
			wantMap: map[string]any{
				"parent": map[string]any{
					"child": "new",
				},
			},
		},
		{
			name:    "set numeric value",
			initial: map[string]any{},
			path:    "count",
			value:   42,
			wantMap: map[string]any{"count": 42},
		},
		{
			name:    "set object value",
			initial: map[string]any{},
			path:    "config",
			value:   map[string]any{"nested": "value"},
			wantMap: map[string]any{"config": map[string]any{"nested": "value"}},
		},
		{
			name:    "error when path through non-map",
			initial: map[string]any{"str": "value"},
			path:    "str.key",
			value:   "new",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := copyMap(tt.initial)
			err := setPath(data, tt.path, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("setPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(data, tt.wantMap) {
				t.Errorf("setPath() result = %v, want %v", data, tt.wantMap)
			}
		})
	}
}

// copyMap creates a deep copy of a map[string]any.
func copyMap(m map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range m {
		if nested, ok := v.(map[string]any); ok {
			result[k] = copyMap(nested)
		} else {
			result[k] = v
		}
	}
	return result
}
