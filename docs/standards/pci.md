# PCI Compliance Standards

## Scope

MLMForge is a service provider. It is never the merchant of record.

The deploying company is the merchant. That company brings its own payment
provider, and that company owns its own Self-Assessment Questionnaire.

This document states what the platform does with cardholder and bank data, and
what it requires of any payment provider a client brings. A client's compliance
team reads it to assess whether and how MLMForge sits inside that client's
compliance scope.

## Status of this document

This standard records decisions that already exist. It does not make them and it
does not ratify them.

No compliance review has assessed any decision recorded here. A security
compliance team ratifies this document before any payment processing
implementation begins.

As of this commit, on 2026-09-13, no payment processing exists in the platform.
The `internal/financial` package declares interfaces and types. Nothing
implements them. This observation carries a date because it stops being true
when payment work lands, and unblocking that work is why this standard exists.

## Provenance

The owner confirmed the service provider position on 2026-09-12. The integration
model recorded here was chosen earlier, during the inception-era bounded-context
analysis of the legacy osMLM system. It was then written into Go doc comments
inside `internal/financial` rather than into a standard. HEU-618 raised that gap
and holds the references to that analysis.

Paths into other repositories are deliberately absent from this document.
Nothing in this repository can keep such a path true. A client's assessor reading
this file holds the product repository and does not hold the others, so a path
that looks like evidence and cannot be followed is worse than no citation.

### The owner's direction

These are the owner's words:

> "We'd want to tokenize credit cards and use that token for recurring payments,
> or use a third party payment processor like stripe, PayPal, etc. Basically the
> risk shouldn't live with us on this."

Two things about that quote.

**Stripe and PayPal name a category, not a selection.** No provider has been
chosen. The deploying client picks one.

**"The risk shouldn't live with us" is the principle, not a control.** The
requirements in this document follow from it. The sentence itself is not
testable, and nothing should be assessed against it.

---

Tracked in [HEU-618](https://linear.app/heuss-enterprises/issue/HEU-618).
