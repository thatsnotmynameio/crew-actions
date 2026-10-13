package spec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	content := []byte(`steps:
  - name: build
    commands:
      - cd src
      - make
  - name: check
    commands:
      - ./scripts/check.sh
      - echo done
`)
	name, err := NameFromPath("ci/test.yaml")
	if err != nil {
		t.Fatalf("NameFromPath: %v", err)
	}
	got, err := Parse(name, content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Workflow{Name: "test", Steps: []Step{
		{Name: "build", Commands: []string{"cd src", "make"}},
		{Name: "check", Commands: []string{"./scripts/check.sh", "echo done"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}

func TestParseKeepsCommandsAsWritten(t *testing.T) {
	content := []byte("steps:\n  - name: a\n    commands:\n      - 123\n      - true\n      - \"echo key: value\"\n")
	got, err := Parse("w", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"123", "true", "echo key: value"}
	if len(got.Steps) != 1 || !reflect.DeepEqual(got.Steps[0].Commands, want) {
		t.Errorf("Parse = %+v, want one step with the commands %q", got, want)
	}
}

func TestParseFollowsAliases(t *testing.T) {
	content := []byte("steps:\n  - name: build\n    commands:\n      - &make make\n" +
		"  - name: again\n    commands:\n      - *make\n")
	got, err := Parse("w", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Step{{Name: "build", Commands: []string{"make"}}, {Name: "again", Commands: []string{"make"}}}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Errorf("Parse = %+v, want the steps %+v", got.Steps, want)
	}
}

func TestNameFromPath(t *testing.T) {
	for path, want := range map[string]string{
		"test.yaml":      "test",
		"test.yml":       "test",
		"./ci/test.yaml": "test",
		"a.b.yaml":       "a.b",
	} {
		got, err := NameFromPath(path)
		if err != nil || got != want {
			t.Errorf("NameFromPath(%q) = %q, %v, want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"test", "test.json", "test.YAML", ".yaml"} {
		if got, err := NameFromPath(path); err == nil {
			t.Errorf("NameFromPath(%q) = %q, want an error", path, got)
		}
	}
}

func TestParseRefusesKeys(t *testing.T) {
	tests := []struct {
		name, content string
		want          KeyError
	}{
		{
			name:    "step key",
			content: "steps:\n  - name: build\n    comands:\n      - make\n",
			want:    KeyError{Key: "comands", Path: "steps[0].comands", Line: 3},
		},
		{
			name:    "top-level key",
			content: "steps:\n  - name: build\n    commands: [make]\ndepends_on: [lint]\n",
			want:    KeyError{Key: "depends_on", Path: "depends_on", Line: 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("w", []byte(tc.content))
			var keyErr *KeyError
			if !errors.As(err, &keyErr) {
				t.Fatalf("Parse error = %v, want a *KeyError", err)
			}
			if *keyErr != tc.want {
				t.Errorf("Parse error = %+v, want %+v", *keyErr, tc.want)
			}
		})
	}
}

func TestKeyErrorMessage(t *testing.T) {
	err := &KeyError{Key: "comands", Path: "steps[0].comands", Line: 3}
	if got, want := err.Error(), "steps[0].comands (line 3): unknown key"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// refusals are files the format refuses, each with the path and line its
// error names and a part of its message.
var refusals = []struct {
	name, content string
	path          string
	line          int
	msg           string
}{
	{name: "empty file", content: "", line: 1, msg: "no workflow"},
	{name: "only comments", content: "# nothing\n# here\n", line: 1, msg: "no workflow"},
	{name: "null document", content: "null\n", line: 1, msg: "no workflow"},
	{name: "top-level list", content: "- name: build\n", line: 1, msg: "mapping"},
	{name: "top-level scalar", content: "build\n", line: 1, msg: "mapping"},
	{
		name:    "two documents",
		content: "steps:\n  - name: a\n    commands: [x]\n---\nsteps:\n  - name: b\n    commands: [y]\n",
		line:    4,
		msg:     "one YAML document",
	},
	{name: "missing steps", content: "{}\n", path: "steps", line: 1, msg: "missing"},
	{name: "null steps", content: "steps:\n", path: "steps", line: 1, msg: "list"},
	{name: "no steps", content: "steps: []\n", path: "steps", line: 1, msg: "list"},
	{
		name:    "null step",
		content: "steps:\n  - name: a\n    commands: [x]\n  -\n",
		path:    "steps[1]",
		line:    4,
		msg:     "mapping",
	},
	{name: "missing name", content: "steps:\n  - commands: [x]\n", path: "steps[0].name", line: 2, msg: "missing"},
	{name: "empty name", content: "steps:\n  - name: \"\"\n    commands: [x]\n", path: "steps[0].name", line: 2},
	{name: "blank name", content: "steps:\n  - name: \"  \"\n    commands: [x]\n", path: "steps[0].name", line: 2},
	{
		name:    "name with a newline",
		content: "steps:\n  - name: \"a\\nb\"\n    commands: [x]\n",
		path:    "steps[0].name",
		line:    2,
		msg:     "control",
	},
	{
		name:    "duplicate step name",
		content: "steps:\n  - name: a\n    commands: [x]\n  - name: a\n    commands: [y]\n",
		path:    "steps[1].name",
		line:    4,
		msg:     "line 2",
	},
	{name: "missing commands", content: "steps:\n  - name: a\n", path: "steps[0].commands", line: 2, msg: "missing"},
	{
		name:    "no commands",
		content: "steps:\n  - name: a\n    commands: []\n",
		path:    "steps[0].commands",
		line:    3,
		msg:     "list",
	},
	{
		name:    "scalar commands",
		content: "steps:\n  - name: a\n    commands: \"echo hi\"\n",
		path:    "steps[0].commands",
		line:    3,
		msg:     "list",
	},
	{
		name:    "null command",
		content: "steps:\n  - name: a\n    commands:\n      -\n",
		path:    "steps[0].commands[0]",
		line:    4,
	},
	{
		name:    "empty command",
		content: "steps:\n  - name: a\n    commands:\n      - \"\"\n",
		path:    "steps[0].commands[0]",
		line:    4,
	},
	{
		name:    "blank command",
		content: "steps:\n  - name: a\n    commands:\n      - \"   \"\n",
		path:    "steps[0].commands[0]",
		line:    4,
	},
	{
		name:    "command read as a mapping",
		content: "steps:\n  - name: a\n    commands:\n      - make\n      - echo key: value\n",
		path:    "steps[0].commands[1]",
		line:    5,
		msg:     "quote",
	},
	{
		name:    "command with a NUL byte",
		content: "steps:\n  - name: a\n    commands:\n      - \"a\\0b\"\n",
		path:    "steps[0].commands[0]",
		line:    4,
		msg:     "NUL",
	},
	{
		name:    "duplicate key",
		content: "steps:\n  - name: a\n    commands: [x]\n    name: b\n",
		path:    "steps[0].name",
		line:    4,
		msg:     "duplicate key",
	},
}

func TestParseRefuses(t *testing.T) {
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("w", []byte(tc.content))
			var specErr *Error
			if !errors.As(err, &specErr) {
				t.Fatalf("Parse error = %v, want a *Error", err)
			}
			if specErr.Path != tc.path || specErr.Line != tc.line {
				t.Errorf("Parse error at %q line %d, want %q line %d: %v", specErr.Path, specErr.Line, tc.path, tc.line, err)
			}
			if !strings.Contains(specErr.Msg, tc.msg) {
				t.Errorf("Parse error = %q, want it to mention %q", specErr.Msg, tc.msg)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		err  *Error
		want string
	}{
		{err: &Error{Path: "steps", Line: 1, Msg: "missing"}, want: "steps (line 1): missing"},
		{err: &Error{Line: 4, Msg: "a second document"}, want: "line 4: a second document"},
	}
	for _, tc := range tests {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestParseRefusesInvalidYAML(t *testing.T) {
	_, err := Parse("w", []byte("steps: [\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("Parse error = %v, want the YAML error with its line", err)
	}
}
