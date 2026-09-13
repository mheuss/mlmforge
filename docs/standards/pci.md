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

Definitions that restate a PCI DSS term are quoted verbatim, from the PCI SSC
Glossary retrieved 2026-09-13 or from a numbered PCI SSC FAQ article. Four terms
are not defined from the Glossary. Two of those are called out because the
Glossary does hold an entry for the word, in a different sense from the one this
document uses. The other two have no Glossary entry at all.

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

### Terms the Glossary defines in a different sense

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

The distinction is load-bearing. The storage rules in this document depend on
the payment sense, because they turn on what may be retained after
authorization. The access-control sense has no "after".

#### Token, in the payment sense

The glossary's "Token" entry covers authentication: "a value provided by
hardware or software that works with an authentication server or VPN to perform
dynamic or multi-factor authentication." This document never uses that sense.

The PCI SSC does define the sense this document uses, outside the Glossary. It
distinguishes three kinds of token, and only one of them is the kind MLMForge
handles. FAQ 1384, April 2016: "Acquiring tokens are created by the acquirer,
merchant, or a merchant's service provider after the cardholder presents their
PAN and/or other payment credentials."

The same article separates that from an EMVCo Payment Token: "Payment tokens are
created by TSPs that are registered with EMVCo." A Payment Token is a defined
term belonging to a different scheme. This document does not use it, and the
capitalised form should not be read into any sentence here.

Wherever this document says token, it means an acquiring token. The same article
states what one is for: "Acquiring Tokens cannot be used for new authorizations.
They can be used for card-on-file and recurring payments." That is the use this
platform has.

What a token must additionally satisfy to be acceptable here is a requirement
rather than a definition. This section does not state it.

### Terms with no PCI DSS definition

Neither term below appears in the PCI SSC Glossary, which is the only PCI SSC
source searched for them. Both are defined here for this document's use only.

#### Merchant of record

The entity that accepts the payment card as payment for goods or services.

Whether a given party meets the PCI DSS definition of a merchant in a given
deployment is that party's determination to make with its acquirer. This
document does not make it for anyone.

#### Payment provider

This document's umbrella term for whichever external party a deploying client
selects to tokenize cards and process transactions.

The term is deliberately broad and it is not a claim that the kinds it covers
are equivalent. A payment gateway, a payment service provider and a full
processor differ in ways that can affect a deploying client's own assessment.
Where a requirement in this document binds all of them alike, that requirement
says so.

## What MLMForge requires of any payment provider

The deploying client selects the payment provider. This section states what that
provider must satisfy. It does not describe a provider, and it does not describe
an integration topology, because neither has been chosen.

### The requirements

Any payment provider a client brings must satisfy all five.

1. A PAN is tokenized before it reaches any MLMForge server. This holds whatever
   renders the card entry fields.
2. MLMForge receives a token and never a PAN. The field that carries it is
   `PaymentMethodInput.GatewayToken`.
3. The token is what MLMForge stores, and the token is what MLMForge presents to
   charge. Recurring charges reuse the stored token rather than re-collecting a
   card.
4. The token is useless to anyone who holds it without the provider. A token
   that can be reversed to a PAN outside the provider is not a token for the
   purpose of this document.
5. The provider abstraction stays inside `internal/financial`. No consumer of
   that package learns which provider a deployment uses.

Requirement 1 is the boundary the rest of this document rests on. If a PAN
reaches an MLMForge server, no control applied after that point can put it back
outside.

Requirement 4 is the one most easily assumed rather than checked. A token is not
irreversible because it is called a token. Some tokenization solutions are
format-preserving and combine truncation with a reversible transformation of the
remaining digits. PCI SSC FAQ 1117, September 2021, lists the factors that keep
such a value in scope. The first is this: "The tokenization or encryption of the
PAN segment can be reversed in the environment in which the segment resides."

### Acceptance, for the deploying client

A client's compliance team can tick these against a candidate provider. Every
one of them is a question the provider can answer.

- [ ] A PAN is tokenized before it reaches any MLMForge server, under the
      topology this deployment uses.
- [ ] The provider issues a token that MLMForge can store and present to charge.
- [ ] The token is not reversible to a PAN by anyone holding it without the
      provider.
- [ ] The token supports the charges this deployment makes, including recurring
      charges, without re-collecting a card.
- [ ] Nothing in the integration requires a consumer of `internal/financial` to
      know which provider is in use.

### What these requirements deliberately do not settle

They do not settle where tokenization happens. A provider may render the card
entry fields in an iframe it hosts, redirect the cardholder to a page it hosts,
or supply client code that posts the card directly to it.

Those three differ, and the difference is not cosmetic. It affects the deploying
client's own assessment obligations. The client chooses the provider, so the
client chooses among them. No choice has been made here, and a reader should not
infer one.

The requirements above hold identically under all three. That is why they are
written as requirements rather than as a description.

### Cardholder data flow

The boundary in step 3 is the one that matters. Everything above it is the
provider's. Everything below it is MLMForge's.

1. The cardholder enters card details. Which surface collects them depends on
   the topology the client selected.
2. Those details go to the payment provider.
3. **The card details do not transit an MLMForge server.** This is the boundary.
4. The provider returns a token.
5. The token reaches MLMForge as `PaymentMethodInput.GatewayToken`, through
   `WalletManager.Add`.
6. MLMForge stores the token together with descriptive fields. A stored
   `PaymentMethod` carries an instrument type, a truncated value, a card expiry
   where the instrument is a card, and a user-facing label.
7. To charge, MLMForge names a saved payment method. `ChargeRequest` carries
   `PaymentMethodID`, which references the stored method rather than any card
   value.
8. MLMForge receives a `ChargeResult` carrying a transaction identifier, a
   status, and a provider reference. It carries no card value.

Steps 1 through 4 are the provider's. Steps 5 through 8 are MLMForge's. A PAN
appears only in steps 1 and 2, and only inside the provider's boundary.

---

Tracked internally in
[HEU-618](https://linear.app/heuss-enterprises/issue/HEU-618).
