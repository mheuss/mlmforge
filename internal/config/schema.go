package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mlmforge/mlmforge/internal/dec"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"gopkg.in/yaml.v3"
)

// validateSchema checks YAML bytes against the JSON Schema.
// It returns a slice of ValidationError for each schema violation found.
// An empty slice means the document passed schema validation.
func (p *Pipeline) validateSchema(yamlBytes []byte) []ValidationError {
	// This decode must run before the node walk. It is what refuses an alias bomb.
	var discard any
	if err := yaml.Unmarshal(yamlBytes, &discard); err != nil {
		return []ValidationError{{
			Path:     "",
			Code:     "yaml_parse_error",
			Message:  err.Error(),
			Severity: SeverityError,
		}}
	}
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

// maxYAMLNodes bounds how many nodes and merges conversion may visit.
const maxYAMLNodes = 1_000_000

// maxYAMLDepth bounds nesting, counting each alias and merge followed as a level.
const maxYAMLDepth = 64

// maxYAMLErrors bounds how many errors conversion records before it stops.
const maxYAMLErrors = 100

type yamlConverter struct {
	errs     []ValidationError
	active   map[*yaml.Node]bool
	reported map[*yaml.Node]bool
	keys     map[*yaml.Node]yamlKey
	visited  uint
	depth    uint
	tooDeep  bool
	stopped  bool
}

type yamlKey struct {
	text string
	ok   bool
}

// yamlPath is a JSON Pointer built only when an error needs it.
type yamlPath struct {
	parent  *yamlPath
	segment string
}

func (p *yamlPath) child(segment string) *yamlPath {
	return &yamlPath{parent: p, segment: segment}
}

func (p *yamlPath) String() string {
	var segments []string
	for at := p; at != nil; at = at.parent {
		segments = append(segments, at.segment)
	}
	var b strings.Builder
	for i := len(segments) - 1; i >= 0; i-- {
		b.WriteString("/")
		b.WriteString(segments[i])
	}
	return b.String()
}

// yamlNodeToJSON converts a decoded YAML tree into the value shapes the
// schema validator reads. Numbers become json.Number holding their exact text.
func yamlNodeToJSON(root *yaml.Node) (any, []ValidationError) {
	c := &yamlConverter{
		active:   map[*yaml.Node]bool{},
		reported: map[*yaml.Node]bool{},
		keys:     map[*yaml.Node]yamlKey{},
	}
	v := c.node(root, nil)
	return v, c.errs
}

// record appends an error until the cap, then records the cap once and stops conversion.
func (c *yamlConverter) record(path *yamlPath, code, message string) {
	if c.stopped {
		return
	}
	if len(c.errs) == maxYAMLErrors {
		c.errs = append(c.errs, ValidationError{
			Path:     path.String(),
			Code:     "too_many_errors",
			Message:  fmt.Sprintf("conversion stopped after %d errors", maxYAMLErrors),
			Severity: SeverityError,
		})
		c.stopped = true
		return
	}
	c.errs = append(c.errs, ValidationError{Path: path.String(), Code: code, Message: message, Severity: SeverityError})
}

// fail records one error per source node, so alias expansion does not repeat it.
func (c *yamlConverter) fail(n *yaml.Node, path *yamlPath, code, message string) {
	if c.reported[n] {
		return
	}
	c.reported[n] = true
	c.record(path, code, message)
}

// spend counts one step against the budget and reports whether conversion may go on.
func (c *yamlConverter) spend(path *yamlPath) bool {
	if c.stopped {
		return false
	}
	c.visited++
	if c.visited > maxYAMLNodes {
		c.record(path, "document_too_large", fmt.Sprintf("the document expands to more than %d nodes", maxYAMLNodes))
		c.stopped = true
		return false
	}
	return true
}

// descend enters one level of nesting and reports whether the limit allows it.
func (c *yamlConverter) descend(path *yamlPath) bool {
	c.depth++
	if c.depth <= maxYAMLDepth {
		return true
	}
	if !c.tooDeep {
		c.tooDeep = true
		c.record(path, "document_too_deep", fmt.Sprintf("nesting reached depth %d; the limit is %d", c.depth, maxYAMLDepth))
	}
	return false
}

func (c *yamlConverter) ascend() {
	c.depth--
}

// alias follows an alias node, refusing one that refers to itself.
func (c *yamlConverter) alias(n *yaml.Node, path *yamlPath, follow func(*yaml.Node) any) any {
	if c.active[n.Alias] {
		c.fail(n, path, "alias_cycle", "an alias refers to a node that contains it")
		return nil
	}
	c.active[n.Alias] = true
	defer delete(c.active, n.Alias)
	return follow(n.Alias)
}

func (c *yamlConverter) node(n *yaml.Node, path *yamlPath) any {
	if !c.spend(path) {
		return nil
	}
	defer c.ascend()
	if !c.descend(path) {
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
			out[i] = c.node(item, path.child(strconv.Itoa(i)))
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

func (c *yamlConverter) mapping(n *yaml.Node, path *yamlPath) map[string]any {
	out := make(map[string]any, len(n.Content)/2)
	c.fill(n, path, out)
	return out
}

// fill applies the merge rule: it adds each key of n that out does not already
// hold, then the keys of n's merges, the first merged mapping winning.
func (c *yamlConverter) fill(n *yaml.Node, path *yamlPath, out map[string]any) {
	seen := make(map[string]bool, len(n.Content)/2)
	var merge *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !c.spend(path) {
			return
		}
		if isMergeKey(k) {
			if merge != nil {
				c.fail(k, path.child("<<"), "duplicate_key", "mapping key \"<<\" appears more than once")
				continue
			}
			merge = v
			continue
		}
		key, ok := c.key(k, path)
		if !ok {
			continue
		}
		at := path.child(shortKey(key))
		if seen[key] {
			c.fail(k, at, "duplicate_key", fmt.Sprintf("mapping key %s appears more than once after conversion to JSON", quoteKey(key)))
			continue
		}
		seen[key] = true
		value := c.node(v, at)
		if _, exists := out[key]; !exists {
			out[key] = value
		}
	}
	if merge != nil {
		c.merge(merge, path, out)
	}
}

// maxPathKey bounds how much of a key a path or message repeats.
const maxPathKey = 64

// shortKey cuts a long key to its first maxPathKey bytes, on a rune boundary.
func shortKey(key string) string {
	if len(key) <= maxPathKey {
		return key
	}
	cut := maxPathKey
	for cut > 0 && !utf8.RuneStart(key[cut]) {
		cut--
	}
	return key[:cut]
}

// quoteKey quotes a key for a message, stating the full length outside the
// quotes when the key was cut.
func quoteKey(key string) string {
	if len(key) <= maxPathKey {
		return strconv.Quote(key)
	}
	return fmt.Sprintf("%q... (%d bytes)", shortKey(key), len(key))
}

func (c *yamlConverter) merge(m *yaml.Node, path *yamlPath, out map[string]any) {
	if m.Kind != yaml.SequenceNode {
		c.mergeOne(m, path, out)
		return
	}
	for _, item := range m.Content {
		c.mergeOne(item, path, out)
	}
}

// mergeOne accepts a mapping or an alias to a mapping and refuses any other node.
func (c *yamlConverter) mergeOne(m *yaml.Node, path *yamlPath, out map[string]any) {
	if !c.spend(path) {
		return
	}
	defer c.ascend()
	if !c.descend(path) {
		return
	}
	target := m
	if m.Kind == yaml.AliasNode {
		target = m.Alias
	}
	if target == nil || target.Kind != yaml.MappingNode {
		c.fail(m, path, "invalid_merge", "a << merge must name a mapping or a list of mappings")
		return
	}
	apply := func(target *yaml.Node) any {
		c.fill(target, path, out)
		return nil
	}
	if m.Kind == yaml.AliasNode {
		c.alias(m, path, apply)
		return
	}
	apply(m)
}

// key converts a mapping key once per source node, however many aliases reach it.
func (c *yamlConverter) key(k *yaml.Node, path *yamlPath) (string, bool) {
	if k.Kind == yaml.AliasNode {
		k = k.Alias
	}
	if cached, done := c.keys[k]; done {
		return cached.text, cached.ok
	}
	text, ok := c.convertKey(k, path)
	c.keys[k] = yamlKey{text: text, ok: ok}
	return text, ok
}

func (c *yamlConverter) convertKey(k *yaml.Node, path *yamlPath) (string, bool) {
	if k.Kind != yaml.ScalarNode {
		c.fail(k, path, "invalid_key", "a mapping key must be a scalar")
		return "", false
	}
	var v any
	if err := k.Decode(&v); err != nil {
		c.fail(k, path, "invalid_key", err.Error())
		return "", false
	}
	return stringifyKey(v), true
}

func (c *yamlConverter) scalar(n *yaml.Node, path *yamlPath) any {
	switch n.ShortTag() {
	case "!!int", "!!float":
		if _, err := dec.Parse(n.Value); err != nil {
			c.fail(n, path, "invalid_number", err.Error())
			return nil
		}
		return json.Number(n.Value)
	default:
		var v any
		if err := n.Decode(&v); err != nil {
			c.fail(n, path, "yaml_parse_error", err.Error())
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
