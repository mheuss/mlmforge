// Package financial provides payment processing, wallet management, and
// invoicing. Uses gateway tokenization — no raw card or bank data stored.
// Consumes domain events for income recording (ADR-010). Handles
// base-currency-to-payout-currency conversion at disbursement.
package financial
