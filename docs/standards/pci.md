# PCI Compliance Standards

Draft, written 2026-09-13. No compliance review has assessed this document. Read
"Status of this document" before relying on any sentence in it.

## Scope

MLMForge does not accept payment cards as payment for goods or services. It
contains no code that does, and this document requires that it never gains any.
The deploying company accepts the payment.

- The deploying company brings its own payment provider.
- The deploying company determines its own Self-Assessment Questionnaire, with
  the entity that receives it.
- Which party is the merchant of record follows from the contracts between the
  deploying company and its acquirer. This document does not settle that, and
  software cannot settle it. What this document states is that MLMForge is built
  so as not to be that party.

This document states what the platform does with cardholder and bank data, and
what it requires of any payment provider a client brings. It is written to
support a deploying client's compliance assessment. It is not itself an
assessment.

## Status of this document

- This standard states requirements on MLMForge's own behaviour. Those are
  decisions MLMForge is entitled to make about its own code, and some of them
  are made here for the first time.
- It records rather than ratifies any determination that belongs to someone
  else. A PCI DSS classification, an SAQ type, and an assessor's conclusion are
  not this document's to make, and it does not make them.
- No compliance review has assessed anything written here.

**A security compliance team must ratify this document before any payment
processing implementation begins.**

- [ ] A security compliance team has ratified this document.

### No payment processing exists yet, and how to recheck that

On 2026-09-13, at this commit, nothing in the platform charges a payment. Three
checks support that statement for the Go code. Run them rather than trusting the
date. Each prints the number of files searched beside its result, because a zero
with no denominator is not evidence.

`internal/financial` declares three interfaces, `PaymentProcessor`,
`WalletManager` and `InvoiceProvider`. Only that file names them, and no Go file
implements any of them.

```bash
find cmd internal -name '*.go' -print0 | xargs -0 -n1 echo | wc -l
find cmd internal -name '*.go' -print0 \
  | xargs -0 grep -l 'PaymentProcessor\|WalletManager\|InvoiceProvider'
```

Expected: a file count, then `internal/financial/interfaces.go` and nothing
else. On 2026-09-13 the count was 116. Design documentation under `content/`
does reference the three names, which is why the search is scoped to Go files.

Nothing under `cmd/` or `internal/` imports `net/http`.

```bash
find cmd internal -name '*.go' -print0 | xargs -0 grep -l '"net/http"' | wc -l
```

Expected: 0.

Use `find -print0` piped to `xargs -0` rather than a bare recursive grep. A
recursive grep may be shell-aliased or gitignore-aware, in which case an empty
result means the files were never read rather than that nothing matched.

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

The product owner gave this direction in conversation during this ticket's plan
review, in September 2026. It is not recorded in any durable artifact. The
wording below reached this document by way of the implementation plan, and
whether it is word-for-word what the owner wrote cannot be established from
anything this repository holds.

It is reproduced as a quotation because that is how it was handed over. Read it
as the substance of the direction, not as a transcript.

> "We'd want to tokenize credit cards and use that token for recurring payments,
> or use a third party payment processor like stripe, PayPal, etc. Basically the
> risk shouldn't live with us on this."

Two things about it.

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
capitalized form should not be read into any sentence here.

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

## The provider boundary

The deploying client selects the payment provider. This section states what that
provider must satisfy, and what MLMForge commits to in return. It does not
describe a provider, and it does not describe an integration arrangement,
because neither has been chosen.

No code implements any of this yet. See "No payment processing exists yet" above
before reading the Go symbol names below as descriptions of running code.

### What the provider must satisfy

1. A PAN never reaches an MLMForge server. The provider tokenizes it first,
   whatever renders the card entry fields.
2. Tokenization happens on the provider's systems.
3. The provider issues a token that cannot be reversed to a PAN by anyone who
   does not hold the provider's own secrets. A token that can be reversed
   outside the provider does not satisfy this requirement.
4. The token supports card-on-file and recurring charges without re-collecting a
   card.
5. No field the provider populates carries a PAN or sensitive authentication
   data. This includes free-text fields such as a decline reason.

Requirement 1 is the boundary the rest of this document rests on. If a PAN
reaches an MLMForge server, no control applied after that point can put it back
outside.

Requirement 3 is the one most easily assumed rather than checked. A token is not
irreversible because it is called a token. Some tokenization solutions are
format-preserving and combine truncation with a reversible transformation of the
remaining digits. PCI SSC FAQ 1117, September 2021, lists factors that keep such
a value in scope for PCI DSS. Among them: "The tokenization or encryption of the
PAN segment can be reversed in the environment in which the segment resides."

Requirement 5 exists because a provider writes that field, not MLMForge. A
decline reason is free text chosen by the provider. MLMForge cannot stop a
provider putting a card number into one.

MLMForge can stop the consequence, and it does. See the commitment on decline
reasons below. The requirement and the commitment are both needed. One asks the
provider not to send it. The other means it does not matter if they do.

### What MLMForge commits to

- MLMForge accepts a token and never a PAN. `PaymentMethodInput.GatewayToken` is
  the field that carries it in.
- MLMForge presents the token to charge, for card-on-file and recurring charges.
- MLMForge stores no PAN and no sensitive authentication data of its own.
- MLMForge does not persist or log a provider decline reason verbatim. It
  records the status and the provider reference, which are machine-generated,
  and discards the free text. This is what makes requirement 5 a control rather
  than a hope.
- The provider abstraction stays inside `internal/financial`. No consumer of
  that package learns which provider a deployment uses. This is a portability
  commitment rather than a data protection control, and it is recorded here
  because it was previously asserted only in a Go doc comment.
- MLMForge does not tokenize. It has no code that turns a PAN into a token and
  this document forbids adding any.

### Acceptance, for the deploying client

A client's compliance team can tick these for a given deployment. This document
states the rules. It cannot tell anyone whether a particular provider and
deployment meet them. That is what these three boxes ask.

- [ ] Under the arrangement this deployment actually uses, no PAN reaches an
      MLMForge server.
- [ ] The provider has stated, in a document the client holds, that its tokens
      cannot be reversed to a PAN outside the provider's environment.
- [ ] No component the client places between the cardholder and the provider
      tokenizes a PAN. Any such component sits in the client's own compliance
      scope and this document does not cover it.

There is deliberately no box asking a provider to warrant that no field it
populates carries a PAN. Requirement 5 asks the provider not to send one, and
the commitment above means it does not matter if one arrives, because MLMForge
discards that free text rather than storing it. A control MLMForge owns is
worth more here than an attestation from a provider nobody has chosen.

### What these requirements deliberately do not settle

They do not settle which surface collects the card. A provider may offer any of
three arrangements.

- Hosted iframe. The provider renders the entry fields in a frame it serves.
- Redirect. The provider takes the cardholder to a page it serves.
- Provider client code. The provider supplies code that runs in a page MLMForge
  serves and posts the card directly to the provider.

Tokenization itself always happens on the provider's systems. What varies is
what the cardholder is looking at when they type.

Those three differ, and the difference is not cosmetic. It affects the deploying
client's own assessment obligations. The client chooses the provider. The client
therefore chooses among these three. No choice has been made here, and a reader
should not infer one.

The requirements above hold identically under all three. That is why they are
written as requirements rather than as a description.

They also do not settle where MLMForge holds the token at rest. `PaymentMethod`,
the stored type, currently declares no token field, so no persistence model has
been chosen. Nothing in this document depends on which one is. The boundary, the
prohibitions and the irreversibility requirement all hold wherever the token
sits, because what sits there is a token.

### Cardholder data flow

The boundary is between servers, not between organizations. Under provider
client code the surface that collects the card is served by MLMForge, and the
boundary still holds.

1. The cardholder enters card details. Which surface collects them depends on
   which of the three arrangements the client selected.
2. Those details go to the payment provider. Tokenization happens there.
3. **No PAN reaches an MLMForge server.**
4. The provider returns a token.
5. The token reaches MLMForge as `PaymentMethodInput.GatewayToken`, through
   `WalletManager.Add`.
6. MLMForge holds the token so it can charge later.
7. A stored `PaymentMethod` carries these card-derived fields: an instrument
   type, a truncated value, a card expiry where the instrument is a card, and a
   user-facing label.
8. To charge, MLMForge names a saved payment method. `ChargeRequest` carries
   `PaymentMethodID`, which references the stored method. That is the internal
   call. What MLMForge then sends the provider is the token.
9. MLMForge receives a `ChargeResult`. It carries a transaction identifier, a
   status, a provider reference, and a decline reason. The first three are
   machine-generated identifiers and carry no PAN or sensitive authentication
   data. The fourth is free text, is covered by requirement 5, and is not
   persisted verbatim.

A PAN appears only in steps 1 and 2. No MLMForge server handles it in either of
them, under any of the three arrangements.

Whether the token held from step 5 onward can be reversed to a PAN is
requirement 3. It is a property of the provider the client selects, not
something this platform can establish on its own.

---

Tracked internally in
[HEU-618](https://linear.app/heuss-enterprises/issue/HEU-618).
