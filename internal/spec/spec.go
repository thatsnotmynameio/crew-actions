// Package spec reads a workflow file: one workflow, named after its file, and
// the steps it runs in order.
//
// A workflow file, such as test.yaml for the workflow test, is one YAML
// document whose root is a mapping:
//
//	steps:
//	  - name: build
//	    commands:
//	      - cd src
//	      - make
//	  - name: check
//	    commands:
//	      - ./scripts/check.sh
//
// The workflow has at least one step, and steps is its only key. A step has
// exactly two keys: its name, which is not blank, is unique in the workflow
// and holds no newline or other control character, and its commands, a list
// of at least one. A command is the text written, so `- 123` runs 123. It is
// not blank and holds no NUL byte. An unquoted ": " makes a command a mapping
// in YAML, which is refused: quote such a command. Any other key is refused,
// and every refusal names the path of the value at fault, such as
// steps[1].commands, and its line.
//
// A step's commands run in one POSIX shell, /bin/sh -c, from the workspace's
// root, so a cd or an export carries to the step's next commands but not to
// the next step. After each command the shell exits with that command's
// status when it is not zero, and that status is the step's. A command that
// runs exit 0 ends its step with success and skips the commands after it.
// /bin/sh is dash on Linux and bash on macOS, so bash-only syntax, such as
// [[ or pipefail, fails on Linux.
//
// Each command must be a complete shell command on its own. A heredoc ends
// inside its command, and no command ends with |, && or ||: otherwise the
// commands and checks after it become part of it, and the step can exit 0
// without running them. A command may end with a \ line continuation.
package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// Workflow is one workflow file: its name and its steps, in file order.
type Workflow struct {
	Name  string
	Steps []Step
}

// Step is one step of a workflow: its name and its commands, in file order.
type Step struct {
	Name     string
	Commands []string
}

// Error is a workflow file's refusal: the path of the value at fault, such as
// steps[0].name, empty for the file as a whole, its line, and what is wrong.
type Error struct {
	Path string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	return where(e.Path, e.Line) + e.Msg
}

// KeyError is a key the workflow file's format does not have, with its path,
// such as steps[1].comands, and its line.
type KeyError struct {
	Key  string
	Path string
	Line int
}

func (e *KeyError) Error() string {
	return where(e.Path, e.Line) + "unknown key"
}

// where is the start of every error's message: the path and the line.
func where(at string, line int) string {
	if at == "" {
		return fmt.Sprintf("line %d: ", line)
	}
	return fmt.Sprintf("%s (line %d): ", at, line)
}

// NameFromPath returns the name of the workflow in the file at p: its base
// without the final .yaml or .yml, so ci/test.yaml is test and a.b.yml is
// a.b. Any other extension, none, or nothing before it is an error.
func NameFromPath(p string) (string, error) {
	base := path.Base(p)
	for _, ext := range []string{".yaml", ".yml"} {
		stem, ok := strings.CutSuffix(base, ext)
		if !ok {
			continue
		}
		if stem == "" {
			return "", fmt.Errorf("%s: a workflow file needs a name before %s", p, ext)
		}
		return stem, nil
	}
	return "", fmt.Errorf("%s: a workflow file ends in .yaml or .yml", p)
}

// Parse reads the workflow named name from content, a workflow file's
// bytes. A refusal is a *KeyError for a key the format does not have, an
// *Error for anything else the format refuses, or the YAML parser's error.
func Parse(name string, content []byte) (Workflow, error) {
	root, err := document(content)
	if err != nil {
		return Workflow{}, err
	}
	steps, err := workflowSteps(root)
	if err != nil {
		return Workflow{}, err
	}
	return Workflow{Name: name, Steps: steps}, nil
}

// nullTag is the tag of a YAML value left empty or written null.
const nullTag = "!!null"

// nodesPerEntry is how many nodes each entry takes in a mapping node's
// Content: its key, then its value.
const nodesPerEntry = 2

// document returns the root mapping of content's one YAML document.
func document(content []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var doc yaml.Node
	if err := decoder.Decode(&doc); errors.Is(err, io.EOF) {
		return nil, &Error{Line: 1, Msg: "the file holds no workflow"}
	} else if err != nil {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	var next yaml.Node
	if err := decoder.Decode(&next); err == nil {
		return nil, &Error{Line: next.Line, Msg: "a workflow file holds one YAML document, and a second one starts here"}
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	root := resolve(doc.Content[0])
	switch {
	case root.ShortTag() == nullTag:
		return nil, &Error{Line: root.Line, Msg: "the file holds no workflow"}
	case root.Kind != yaml.MappingNode:
		return nil, &Error{Line: root.Line, Msg: "a workflow must be a mapping, with steps"}
	}
	return root, nil
}

// resolve returns the node an alias n points to, or n itself when it is no
// alias.
func resolve(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}

// entry is one key of a mapping and its value, with the key's path.
type entry struct {
	key, value *yaml.Node
	path       string
}

// fields returns the entries of mapping n, whose path is at, by key. A key
// not in allowed is a *KeyError, and a key given twice is an *Error.
func fields(n *yaml.Node, at string, allowed ...string) (map[string]entry, error) {
	out := make(map[string]entry, len(allowed))
	for i := 0; i+1 < len(n.Content); i += nodesPerEntry {
		key := n.Content[i]
		keyPath := join(at, key.Value)
		if !slices.Contains(allowed, key.Value) {
			return nil, &KeyError{Key: key.Value, Path: keyPath, Line: key.Line}
		}
		if first, ok := out[key.Value]; ok {
			msg := fmt.Sprintf("duplicate key, first set on line %d", first.key.Line)
			return nil, &Error{Path: keyPath, Line: key.Line, Msg: msg}
		}
		out[key.Value] = entry{key: key, value: resolve(n.Content[i+1]), path: keyPath}
	}
	return out, nil
}

// join returns the path of key in the mapping whose path is at.
func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// index returns the path of item i in the list whose path is at.
func index(at string, i int) string {
	return fmt.Sprintf("%s[%d]", at, i)
}

// list returns the items of the list at key in mapping n, whose path is at,
// and the list's path. The key must be there, and its value must be a list of
// at least one item, each an item names.
func list(entries map[string]entry, n *yaml.Node, at, key, item string) ([]*yaml.Node, string, error) {
	e, ok := entries[key]
	if !ok {
		return nil, "", &Error{Path: join(at, key), Line: n.Line, Msg: "missing: a list of at least one " + item}
	}
	if e.value.Kind != yaml.SequenceNode || len(e.value.Content) == 0 {
		return nil, "", &Error{Path: e.path, Line: e.key.Line, Msg: "must be a list of at least one " + item}
	}
	return e.value.Content, e.path, nil
}

// workflowSteps returns the steps of the workflow whose root mapping is root.
func workflowSteps(root *yaml.Node) ([]Step, error) {
	entries, err := fields(root, "", "steps")
	if err != nil {
		return nil, err
	}
	items, at, err := list(entries, root, "", "steps", "step")
	if err != nil {
		return nil, err
	}
	steps := make([]Step, 0, len(items))
	names := make(map[string]int, len(items))
	for i, item := range items {
		stepPath := index(at, i)
		s, nameLine, err := step(resolve(item), stepPath)
		if err != nil {
			return nil, err
		}
		if first, ok := names[s.Name]; ok {
			msg := fmt.Sprintf("duplicate step name %q, first used on line %d", s.Name, first)
			return nil, &Error{Path: join(stepPath, "name"), Line: nameLine, Msg: msg}
		}
		names[s.Name] = nameLine
		steps = append(steps, s)
	}
	return steps, nil
}

// step returns the step n, whose path is at, and the line of its name.
func step(n *yaml.Node, at string) (Step, int, error) {
	if n.Kind != yaml.MappingNode {
		return Step{}, 0, &Error{Path: at, Line: n.Line, Msg: "a step must be a mapping, with a name and commands"}
	}
	entries, err := fields(n, at, "name", "commands")
	if err != nil {
		return Step{}, 0, err
	}
	name, ok := entries["name"]
	if !ok {
		return Step{}, 0, &Error{Path: join(at, "name"), Line: n.Line, Msg: "missing: a step needs a name"}
	}
	if err := checkName(name); err != nil {
		return Step{}, 0, err
	}
	items, commandsPath, err := list(entries, n, at, "commands", "command")
	if err != nil {
		return Step{}, 0, err
	}
	commands := make([]string, 0, len(items))
	for i, item := range items {
		command, err := checkCommand(resolve(item), index(commandsPath, i))
		if err != nil {
			return Step{}, 0, err
		}
		commands = append(commands, command)
	}
	return Step{Name: name.value.Value, Commands: commands}, name.key.Line, nil
}

// checkName refuses a step's name that is blank or holds a control
// character, since each line of the step's output starts with it.
func checkName(name entry) error {
	v := name.value
	if v.Kind != yaml.ScalarNode || v.ShortTag() == nullTag || strings.TrimSpace(v.Value) == "" {
		return &Error{Path: name.path, Line: name.key.Line, Msg: "a step's name must be text that is not blank"}
	}
	if strings.ContainsFunc(v.Value, unicode.IsControl) {
		return &Error{Path: name.path, Line: name.key.Line, Msg: "a step's name must be one line, with no control character"}
	}
	return nil
}

// checkCommand returns the command n, whose path is at, as written.
func checkCommand(n *yaml.Node, at string) (string, error) {
	switch {
	case n.Kind == yaml.MappingNode:
		msg := `a command must be text, and YAML reads this one as a mapping: quote it, as in - "echo key: value"`
		return "", &Error{Path: at, Line: n.Line, Msg: msg}
	case n.Kind != yaml.ScalarNode || n.ShortTag() == nullTag || strings.TrimSpace(n.Value) == "":
		return "", &Error{Path: at, Line: n.Line, Msg: "a command must be text that is not blank"}
	case strings.ContainsRune(n.Value, 0):
		return "", &Error{Path: at, Line: n.Line, Msg: "a command must not hold a NUL byte"}
	}
	return n.Value, nil
}
