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

## Terminology

Every definition below that restates a PCI DSS term is quoted verbatim from the
PCI SSC Glossary, retrieved 2026-09-13. Two terms are not, and both are called
out, because the glossary defines them in a different sense than this document
uses.

### Quoted from the PCI SSC Glossary

#### Primary account number (PAN)

"Unique payment card number (credit, debit, or prepaid cards, etc.) that
identifies the issuer and the cardholder account."

#### Cardholder data (CHD)

"At a minimum, cardholder data consists of the full PAN. Cardholder data may
also appear in the form of the full PAN plus any of the following: cardholder
name, expiration date and/or service code."

#### Sensitive authentication data (SAD)

"Security-related information used to authenticate cardholders and/or authorize
payment card transactions. This information includes, but is not limited to,
card verification codes, full track data (from magnetic stripe or equivalent on
a chip), PINs, and PIN blocks."

#### Truncation

"Method of rendering a full PAN unreadable by removing a segment of PAN data.
Truncation relates to protection of PAN when electronically stored, processed,
or transmitted."

#### Masking

"Method of concealing a segment of PAN when displayed or printed. Masking is
used when there is no business need to view the entire PAN. Masking relates to
protection of PAN when displayed on screens, paper receipts, printouts, etc."

Masking and truncation are not interchangeable. PCI SSC FAQ 1146, July 2025,
states the difference: "Masking refers to concealing certain digits during
display or printing, even when the entire PAN is stored on a system. This
process differs from truncation, in which the truncated digits are removed and
cannot be retrieved within the system."

#### Service provider

"Business entity that is not a payment brand, directly involved in the
processing, storage, or transmission of cardholder data (CHD) and/or sensitive
authentication data (SAD) on behalf of another entity. This includes payment
gateways, payment service providers (PSPs), and independent sales organizations
(ISOs). This also includes companies that provide services that control or could
impact the security of CHD and/or SAD. Examples include managed service
providers that provide managed firewalls, IDS, and other services as well as
hosting providers and other entities."

#### Merchant

"For the purposes of the PCI DSS, a merchant is defined as any entity that
accepts payment cards bearing the logos of any PCI SSC Participating Payment
Brand as payment for goods and/or services."

### Two terms the glossary defines in a different sense

The PCI SSC Glossary contains entries for both words below. Neither entry is the
sense this document uses, and quoting either one here would define the wrong
thing.

#### Authorization, in the payment sense

The glossary's "Authorization" entry covers access control: "the granting of
access or other rights to a user, program, or process." This document never uses
that sense. Throughout this document, authorization means the issuer approving a
card transaction. PCI SSC FAQ 1533, July 2021, uses it that way and describes
what happens: card verification codes "are validated by the issuer during
authorization to give them confidence that the card they issued is being used
for the transaction."

The distinction is load-bearing. The storage prohibition in this document turns
on what may be retained *after* authorization, and the access-control sense has
no "after".

#### Token, in the payment sense

The glossary's "Token" entry covers authentication: "a value provided by
hardware or software that works with an authentication server or VPN to perform
dynamic or multi-factor authentication." This document never uses that sense.

In this document, a token is a value issued by a payment provider that stands in
for a PAN, that MLMForge stores and presents to that provider to charge, and
that cannot be reversed to a PAN by anyone holding it alone. The PCI SSC
Glossary has no entry for tokenization in this sense, so this definition is this
document's own and is not quoted from any standard.

### Terms with no PCI DSS definition

Neither term below appears in the PCI SSC Glossary. Both are defined here for
this document's use only.

#### Merchant of record

The entity that accepts the payment card as payment for goods or services, and
that therefore carries the merchant obligations described above. In every
MLMForge deployment that entity is the deploying company.

#### Payment provider

This document's umbrella term for whichever external party a deploying client
selects to tokenize cards and process transactions. It covers payment gateways,
payment service providers and full processors without distinguishing between
them, because the requirements in this document bind all three identically.

---

Tracked internally in
[HEU-618](https://linear.app/heuss-enterprises/issue/HEU-618).
