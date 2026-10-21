package helm_test

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

func TestHelmValuesDocumentation(t *testing.T) {
	rawValues, err := os.ReadFile(chartPath("values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(rawValues, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		t.Fatal("values.yaml must contain a mapping")
	}
	type documentedValue struct {
		kind, description, defaultCell string
	}
	expected := map[string]documentedValue{}
	roots := map[string]bool{}
	commentPattern := regexp.MustCompile(`(?m)^#\s+(?:\S+\s+)?--\s+(.+)$`)
	var walk func(*yaml.Node, string, string)
	walk = func(node *yaml.Node, path, comments string) {
		matches := commentPattern.FindAllStringSubmatch(comments, -1)
		if len(matches) == 0 {
			t.Errorf("%s (values.yaml:%d) has no explicit helm-docs comment", path, node.Line)
		}
		if node.Kind == yaml.MappingNode && len(node.Content) > 0 {
			for i := 0; i < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				childComments := key.HeadComment + "\n" + value.HeadComment
				// YAML attaches comments before a list item's first key to the mapping.
				if i == 0 {
					childComments = node.HeadComment + "\n" + childComments
				}
				walk(value, path+"."+key.Value, childComments)
			}
			return
		}
		if node.Kind == yaml.SequenceNode && len(node.Content) > 0 {
			for i, value := range node.Content {
				walk(value, fmt.Sprintf("%s[%d]", path, i), value.HeadComment)
			}
			return
		}
		value := documentedValue{}
		if len(matches) > 0 {
			value.description = matches[len(matches)-1][1]
		}
		rawDefault := node.Value
		switch node.Kind {
		case yaml.MappingNode:
			value.kind, rawDefault = "object", "{}"
		case yaml.SequenceNode:
			value.kind, rawDefault = "list", "[]"
		case yaml.ScalarNode:
			switch node.Tag {
			case "!!str":
				value.kind = "string"
				quoted, err := json.Marshal(node.Value)
				if err != nil {
					t.Fatal(err)
				}
				rawDefault = string(quoted)
			case "!!bool":
				value.kind = "bool"
			case "!!int":
				value.kind = "int"
			case "!!float":
				value.kind = "float"
			default:
				t.Fatalf("%s: unsupported scalar tag %s", path, node.Tag)
			}
		default:
			t.Fatalf("%s: unsupported YAML node kind %d", path, node.Kind)
		}
		value.defaultCell = "`" + rawDefault + "`"
		if len(value.defaultCell) > 40 {
			value.defaultCell = fmt.Sprintf("[View default](values.yaml#L%d)", node.Line)
		}
		if _, exists := expected[path]; exists {
			t.Fatalf("duplicate values.yaml path %s", path)
		}
		expected[path] = value
	}
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		roots[key.Value] = true
		walk(value, key.Value, key.HeadComment+"\n"+value.HeadComment)
	}

	rawREADME, err := os.ReadFile(chartPath("README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, valuesSection, found := strings.Cut(string(rawREADME), "\n## Values\n")
	if !found {
		t.Fatal("README.md is missing ## Values")
	}
	valuesSection, _, found = strings.Cut(valuesSection, "\n## Regenerate Documentation\n")
	if !found {
		t.Fatal("README.md is missing ## Regenerate Documentation after ## Values")
	}
	groupPattern := regexp.MustCompile("^Values under `([^`]+)`\\.$")
	settingPattern := regexp.MustCompile("^(`[^`]+`(?:\\.<br>`[^`]+`)*)<br>_\\(([a-z]+)\\)_$")
	seen := map[string]bool{}
	groups := map[string]bool{}
	group := ""
	for _, line := range strings.Split(valuesSection, "\n") {
		if strings.HasPrefix(line, "### ") {
			group = ""
		}
		if match := groupPattern.FindStringSubmatch(line); match != nil {
			group = match[1]
			if !roots[group] {
				t.Errorf("README.md has unknown values group %s", group)
			}
			if groups[group] {
				t.Errorf("README.md repeats values group %s", group)
			}
			groups[group] = true
			continue
		}
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if line == "| Setting | Description | Default |" || line == "|---------|-------------|---------|" {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 5 || cells[0] != "" || cells[4] != "" {
			t.Errorf("README.md values row must have three columns: %s", line)
			continue
		}
		if group == "" {
			t.Errorf("README.md values row has no group: %s", line)
			continue
		}
		setting := strings.TrimSpace(cells[1])
		match := settingPattern.FindStringSubmatch(setting)
		if match == nil {
			t.Errorf("README.md setting must have a relative key and type underneath: %s", setting)
			continue
		}
		relativeKey := strings.ReplaceAll(strings.ReplaceAll(match[1], "<br>", ""), "`", "")
		path := group + "." + relativeKey
		if seen[path] {
			t.Errorf("README.md repeats setting %s", path)
		}
		seen[path] = true
		want, exists := expected[path]
		if !exists {
			t.Errorf("README.md documents unknown setting %s", path)
			continue
		}
		if len(relativeKey) > 30 && strings.Contains(relativeKey, ".") && !strings.Contains(match[1], ".<br>") {
			t.Errorf("%s: long relative key must wrap at dots", path)
		}
		wrappedLines := strings.Split(strings.ReplaceAll(match[1], "`", ""), "<br>")
		for _, wrappedLine := range wrappedLines {
			if len(wrappedLine) > 30 && strings.Contains(strings.TrimSuffix(wrappedLine, "."), ".") {
				t.Errorf("%s: packed key line exceeds 30 characters: %q", path, wrappedLine)
			}
		}
		if path == "api.migration.databaseSecret.name" && match[1] != "`migration.databaseSecret.name`" {
			t.Errorf("short key was unnecessarily wrapped: %s", match[1])
		}
		if path == "api.publicConfig.baseMaps.styleFamilies[0].attribution.text" &&
			match[1] != "`publicConfig.baseMaps`.<br>`styleFamilies[0]`.<br>`attribution.text`" {
			t.Errorf("nested key was not packed and balanced: %s", match[1])
		}
		if match[2] != want.kind {
			t.Errorf("%s: type = %q, want %q", path, match[2], want.kind)
		}
		if description := strings.TrimSpace(cells[2]); description != want.description {
			t.Errorf("%s: description = %q, want %q", path, description, want.description)
		}
		if defaultCell := strings.TrimSpace(cells[3]); defaultCell != want.defaultCell {
			t.Errorf("%s: default = %q, want %q", path, defaultCell, want.defaultCell)
		}
	}
	for path := range expected {
		if !seen[path] {
			t.Errorf("README.md is missing setting %s", path)
		}
	}
	for root := range roots {
		if !groups[root] {
			t.Errorf("README.md is missing values group %s", root)
		}
	}
}
