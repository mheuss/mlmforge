# PCI Compliance Standards

Draft, written 2026-09-13. No compliance review has assessed this document. Read
"Status of this document" before relying on any sentence in it.

## Scope

MLMForge does not accept payment cards as payment for goods or services. The
deploying company does.

- The deploying company is the merchant of record. MLMForge is not, in any
  deployment.
- The deploying company brings its own payment provider.
- The deploying company determines its own Self-Assessment Questionnaire, with
  the entity that receives it.

This document states what the platform does with cardholder and bank data, and
what it requires of any payment provider a client brings. It is written to
support a deploying client's compliance assessment. It is not itself an
assessment.

## Status of this document

- This standard records decisions that already exist. It does not make them and
  it does not ratify them.
- No compliance review has assessed any decision recorded here.
- Nothing here classifies MLMForge under PCI DSS. A classification is a
  determination, and nobody has made one.

**A security compliance team must ratify this document before any payment
processing implementation begins.**

- [ ] A security compliance team has ratified this document.

### No payment processing exists yet, and how to recheck that

On 2026-09-13, at this commit, nothing in the platform charges a payment. Three
checks support that statement for the Go code. Recheck them rather than trusting
the date.

- `internal/financial` declares three interfaces, `PaymentProcessor`,
  `WalletManager` and `InvoiceProvider`. No Go file implements any of them.
- No Go code outside `internal/financial` references those three interfaces.
  Design documentation does reference them.
- Nothing under `cmd/` or `internal/` imports `net/http`.

These three checks cover the Go code only. The Rust engine under `engine/` and
the database migrations are outside their scope and need checking separately.

Other packages name payment concepts in their data models. `internal/commerce`
carries a payment method identifier on its autoship subscription type, and a
gateway dispute identifier on two of its events. Those are structures. Nothing
acts on them.

This observation carries a date because it stops being true when payment work
lands.

## Provenance

Three origins, and they are not the same age. Separating them matters, because
only one of them is a decision and none of them is an approval.

- Tokenization appears in the inception-era bounded-context analysis of the
  legacy osMLM system, as a recommendation rather than a decision. That analysis
  records that the legacy system stored card numbers in its own database, and
  recommends that MLMForge use payment tokens instead.
- The model as it stands is recorded in Go doc comments inside
  `internal/financial`. Version control records who committed those comments.
  Nothing records who made the decision, or whether anyone reviewed it. A
  control asserted in a comment that no audit reads is the defect this standard
  exists to correct.
- The merchant-of-record position was settled in conversation during this
  ticket's plan review, in September 2026. It is not recorded in any durable
  artifact, which is why it is written here. It was not inherited from the
  legacy analysis, which does not discuss it.

This document cites no filesystem path into another repository, because nothing
in this repository can keep such a path true. HEU-618 and the tracking link at
the foot of this file are internal references. An external assessor cannot
follow those either.

### The product owner's direction

These are the product owner's words:

> "We'd want to tokenize credit cards and use that token for recurring payments,
> or use a third party payment processor like stripe, PayPal, etc. Basically the
> risk shouldn't live with us on this."

Two things about that quote.

Stripe and PayPal name a category, not a selection. No provider has been chosen.
The deploying client picks one.

"The risk shouldn't live with us" is the principle, not a control. The
requirements in this document follow from it. The sentence itself is not
testable, so nothing should be assessed against it.

---

Tracked internally in
[HEU-618](https://linear.app/heuss-enterprises/issue/HEU-618).
