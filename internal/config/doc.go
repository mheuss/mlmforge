// Package config validates compensation plan YAML files and translates
// them to JSON matching the Rust engine's expected format.
//
// The pipeline has five steps:
//  1. JSON Schema validation (structural correctness)
//  2. YAML unmarshal to Go structs
//  3. Commission resolution (type-specific second pass)
//  4. Business-rule validation (cross-field logic, referential integrity)
//  5. Structural translation (YAML-shape to Rust-shape JSON)
//
// Schema errors block all subsequent steps. Business-rule errors block
// translation. Warnings do not block.
//
// Usage:
//
//	p, err := config.NewPipeline("schemas/compensation-plan.schema.json")
//	if err != nil { ... }
//	jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)
package config
