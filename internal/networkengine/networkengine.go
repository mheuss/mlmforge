// Package networkengine provides the Go-side interface to the Rust commission
// engine. All interfaces are pure Go — the Rust implementation is an internal
// detail. Consumers have no idea Rust is involved.
//
// Network Engine is currency-free: all volume is in CV points (currency-neutral).
// Commission amounts are in the company's base currency. Commerce handles
// order-currency-to-CV mapping. Financial handles base-to-payout conversion.
package networkengine
