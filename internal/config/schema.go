package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Pipeline validates compensation plan YAML and translates to Rust-ready JSON.
type Pipeline struct {
	schema *jsonschema.Schema
}

// NewPipeline loads and compiles the JSON Schema from the given file path.
// The path can be absolute or relative. The schema must be a valid JSON Schema
// Draft 2020-12 document.
func NewPipeline(schemaPath string) (*Pipeline, error) {
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(schemaPath)
	if err != nil {
		return nil, err
	}
	return &Pipeline{schema: sch}, nil
}

// validateSchema checks YAML bytes against the JSON Schema.
// It returns a slice of ValidationError for each schema violation found.
// An empty slice means the document passed schema validation.
func (p *Pipeline) validateSchema(yamlBytes []byte) []ValidationError {
	var doc any
	if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
		return []ValidationError{{
			Path:     "",
			Code:     "yaml_parse_error",
			Message:  err.Error(),
			Severity: "error",
		}}
	}

	doc = convertYAMLToJSON(doc)

	err := p.schema.Validate(doc)
	if err == nil {
		return nil
	}

	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []ValidationError{{
			Path:     "",
			Code:     "schema_error",
			Message:  err.Error(),
			Severity: "error",
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
			Message:  ve.Error(),
			Severity: "error",
		})
		return
	}
	for _, cause := range ve.Causes {
		collectLeafErrors(cause, out)
	}
}

// buildInstancePath joins instance location tokens into a JSON Pointer path.
// Returns empty string for the root document.
func buildInstancePath(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return "/" + strings.Join(tokens, "/")
}

// convertYAMLToJSON converts YAML-decoded values to JSON Schema-compatible
// types. YAML v3 decodes maps as map[string]interface{}, but numeric keys
// in rate tables may produce map[interface{}]interface{} in some cases.
// YAML also decodes whole numbers as int, while JSON Schema expects float64.
func convertYAMLToJSON(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(val))
		for k, v := range val {
			result[k] = convertYAMLToJSON(v)
		}
		return result
	case map[any]any:
		result := make(map[string]any, len(val))
		for k, v := range val {
			result[stringifyKey(k)] = convertYAMLToJSON(v)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = convertYAMLToJSON(item)
		}
		return result
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
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
