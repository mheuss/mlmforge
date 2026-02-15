package config

// ValidationError represents a single validation issue found during
// compensation plan validation. Errors are structural (JSON Schema) or
// semantic (business-rule). Path uses JSON Pointer syntax.
type ValidationError struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // "error" or "warning"
}
