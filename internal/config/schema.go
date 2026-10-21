package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mlmforge/mlmforge/internal/dec"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"gopkg.in/yaml.v3"
)

// validateSchema checks YAML bytes against the JSON Schema.
// It returns a slice of ValidationError for each schema violation found.
// An empty slice means the document passed schema validation.
func (p *Pipeline) validateSchema(yamlBytes []byte) []ValidationError {
	var root yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &root); err != nil {
		return []ValidationError{{
			Path:     "",
			Code:     "yaml_parse_error",
			Message:  err.Error(),
			Severity: SeverityError,
		}}
	}

	doc, convErrs := yamlNodeToJSON(&root)
	if len(convErrs) > 0 {
		return convErrs
	}

	err := p.schema.Validate(doc)
	if err == nil {
		return nil
	}

	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		// Defensive: jsonschema.Validate normally returns *jsonschema.ValidationError.
		// This branch handles unexpected error types from the library.
		return []ValidationError{{
			Path:     "",
			Code:     "schema_error",
			Message:  err.Error(),
			Severity: SeverityError,
		}}
	}

	return flattenValidationErrors(verr)
}

// flattenValidationErrors walks the ValidationError tree and collects
// leaf errors (those with no causes). Leaf nodes represent actual
// constraint violations rather than intermediate groupings.
func flattenValidationErrors(root *jsonschema.ValidationError) []ValidationError {
	var result []ValidationError
	collectLeafErrors(root, &result)
	return result
}

// collectLeafErrors recursively walks the error tree. Errors with no
// causes are leaf violations. Errors with causes are groupings that
// wrap the real problems.
func collectLeafErrors(ve *jsonschema.ValidationError, out *[]ValidationError) {
	if len(ve.Causes) == 0 {
		path := buildInstancePath(ve.InstanceLocation)
		*out = append(*out, ValidationError{
			Path:     path,
			Code:     "schema_violation",
			Message:  leafMessage(ve),
			Severity: SeverityError,
		})
		return
	}
	for _, cause := range ve.Causes {
		collectLeafErrors(cause, out)
	}
}

// leafMessage returns the message to report for a leaf schema violation.
func leafMessage(ve *jsonschema.ValidationError) string {
	// The library reports only "'not' failed", which names neither field.
	if _, isNot := ve.ErrorKind.(*kind.Not); isNot && strings.HasSuffix(ve.SchemaURL, "#/$defs/StructureQualification") {
		return "max_group_volume_per_leg and max_group_volume_per_leg_fraction must not both be above 0"
	}
	return ve.Error()
}

// buildInstancePath joins instance location tokens into a JSON Pointer path.
// Returns empty string for the root document.
func buildInstancePath(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return "/" + strings.Join(tokens, "/")
}

// maxYAMLNodes bounds how many nodes alias expansion may visit.
const maxYAMLNodes = 1_000_000

type yamlConverter struct {
	errs    []ValidationError
	active  map[*yaml.Node]bool
	visited uint
}

// yamlNodeToJSON converts a decoded YAML tree into the value shapes the
// schema validator reads. Numbers become json.Number holding their exact text.
func yamlNodeToJSON(root *yaml.Node) (any, []ValidationError) {
	c := &yamlConverter{active: map[*yaml.Node]bool{}}
	v := c.node(root, "")
	return v, c.errs
}

func (c *yamlConverter) fail(path, code, message string) {
	c.errs = append(c.errs, ValidationError{Path: path, Code: code, Message: message, Severity: SeverityError})
}

// alias follows an alias node, refusing one that refers to itself.
func (c *yamlConverter) alias(n *yaml.Node, path string, follow func(*yaml.Node) any) any {
	if c.active[n.Alias] {
		c.fail(path, "alias_cycle", "an alias refers to a node that contains it")
		return nil
	}
	c.active[n.Alias] = true
	defer delete(c.active, n.Alias)
	return follow(n.Alias)
}

func (c *yamlConverter) node(n *yaml.Node, path string) any {
	c.visited++
	if c.visited == maxYAMLNodes+1 {
		c.fail(path, "document_too_large", fmt.Sprintf("the document expands to more than %d nodes", maxYAMLNodes))
	}
	if c.visited > maxYAMLNodes {
		return nil
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return c.node(n.Content[0], path)
	case yaml.AliasNode:
		return c.alias(n, path, func(target *yaml.Node) any { return c.node(target, path) })
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, item := range n.Content {
			out[i] = c.node(item, path+"/"+strconv.Itoa(i))
		}
		return out
	case yaml.MappingNode:
		return c.mapping(n, path)
	case yaml.ScalarNode:
		return c.scalar(n, path)
	default:
		return nil
	}
}

func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.ShortTag() == "!!merge")
}

// mapping applies yaml.v3's merge rule: an explicit key wins over a merged
// one, and among merged mappings the first to supply a key wins.
func (c *yamlConverter) mapping(n *yaml.Node, path string) map[string]any {
	out := make(map[string]any, len(n.Content)/2)
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if isMergeKey(k) {
			merges = append(merges, v)
			continue
		}
		key, ok := c.key(k, path)
		if !ok {
			continue
		}
		if _, dup := out[key]; dup {
			c.fail(path+"/"+key, "duplicate_key", fmt.Sprintf("mapping key %q appears more than once after conversion to JSON", key))
			continue
		}
		out[key] = c.node(v, path+"/"+key)
	}
	for _, m := range merges {
		c.merge(m, path, out)
	}
	return out
}

func (c *yamlConverter) merge(m *yaml.Node, path string, out map[string]any) {
	if m.Kind == yaml.AliasNode {
		c.alias(m, path, func(target *yaml.Node) any {
			c.merge(target, path, out)
			return nil
		})
		return
	}
	switch m.Kind {
	case yaml.MappingNode:
		for k, v := range c.mapping(m, path) {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	case yaml.SequenceNode:
		for _, item := range m.Content {
			c.merge(item, path, out)
		}
	default:
		c.fail(path, "invalid_merge", "a << merge must name a mapping or a list of mappings")
	}
}

func (c *yamlConverter) key(k *yaml.Node, path string) (string, bool) {
	if k.Kind == yaml.AliasNode {
		k = k.Alias
	}
	if k.Kind != yaml.ScalarNode {
		c.fail(path, "invalid_key", "a mapping key must be a scalar")
		return "", false
	}
	var v any
	if err := k.Decode(&v); err != nil {
		c.fail(path, "invalid_key", err.Error())
		return "", false
	}
	return stringifyKey(v), true
}

func (c *yamlConverter) scalar(n *yaml.Node, path string) any {
	switch n.ShortTag() {
	case "!!int", "!!float":
		if _, err := dec.Parse(n.Value); err != nil {
			c.fail(path, "invalid_number", err.Error())
			return nil
		}
		return json.Number(n.Value)
	default:
		var v any
		if err := n.Decode(&v); err != nil {
			c.fail(path, "yaml_parse_error", err.Error())
			return nil
		}
		return v
	}
}

// stringifyKey converts a non-string map key to its string representation.
// Used for YAML maps with numeric keys (e.g., rate table levels).
func stringifyKey(k any) string {
	switch v := k.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}
