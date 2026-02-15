package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// LoadAndValidate runs the full validation pipeline on compensation plan YAML.
//
// Returns:
//   - jsonBytes: Rust-ready JSON. Non-nil only when no hard errors exist.
//   - errs: Validation errors and warnings. Empty slice on success.
//   - err: Infrastructure failure (schema load error, unexpected internal error).
//
// The pipeline has five steps:
//  1. Schema validation (JSON Schema Draft 2020-12)
//  2. YAML unmarshal to Go structs
//  3. Commission resolution (type-specific second pass)
//  4. Business-rule validation (cross-field, referential integrity)
//  5. Structural translation (YAML-shape to Rust-shape JSON)
//
// Schema errors block all subsequent steps. Business-rule errors block
// translation. Warnings (severity "warning") do not block.
func (p *Pipeline) LoadAndValidate(yamlBytes []byte) ([]byte, []ValidationError, error) {
	// Step 1: Schema validation.
	schemaErrs := p.validateSchema(yamlBytes)
	if hasErrors(schemaErrs) {
		return nil, schemaErrs, nil
	}

	// Step 2: Unmarshal YAML to Go structs.
	var plan CompensationPlan
	if err := yaml.Unmarshal(yamlBytes, &plan); err != nil {
		return nil, nil, fmt.Errorf("yaml unmarshal: %w", err)
	}

	// Step 3: Commission resolution (type-specific second pass).
	if err := resolveCommissions(&plan); err != nil {
		return nil, nil, fmt.Errorf("commission resolution: %w", err)
	}

	// Step 4: Business-rule validation.
	var allErrs []ValidationError
	allErrs = append(allErrs, schemaErrs...) // carry forward any schema warnings
	ruleErrs := validateBusinessRules(&plan)
	allErrs = append(allErrs, ruleErrs...)

	if hasErrors(allErrs) {
		return nil, allErrs, nil
	}

	// Step 5: Structural translation (YAML-shape to Rust-shape JSON).
	jsonBytes, err := translateToEngine(&plan)
	if err != nil {
		return nil, nil, fmt.Errorf("translation: %w", err)
	}

	return jsonBytes, allErrs, nil
}

// hasErrors returns true if any ValidationError has severity "error".
func hasErrors(errs []ValidationError) bool {
	for _, e := range errs {
		if e.Severity == "error" {
			return true
		}
	}
	return false
}
