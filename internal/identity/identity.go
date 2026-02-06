// Package identity provides user management, addresses, status transitions,
// and authentication. Most depended-upon context (7 inbound dependencies).
// Deliberately excludes sensitive fields — password hash is internal,
// tax ID belongs to Financial, sponsor_id belongs to Network Engine (ADR-009).
package identity
