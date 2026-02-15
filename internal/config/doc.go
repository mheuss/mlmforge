// Package config validates compensation plan YAML files and translates
// them to JSON matching the Rust engine's expected format.
//
// The pipeline has three phases:
//  1. JSON Schema validation (structural correctness)
//  2. Business-rule validation (cross-field logic, referential integrity)
//  3. Structural translation (YAML-shape to Rust-shape JSON)
//
// Usage:
//
//	p, err := config.NewPipeline("schemas/compensation-plan.schema.json")
//	if err != nil { ... }
//	jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)
package config
