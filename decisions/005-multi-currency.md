# 005: Multi-Currency and Internationalization

## The Problem

MLM companies frequently operate internationally. A company might sell products in the US, EU, and Japan with different pricing, different product suites, and different regulatory requirements. The commission engine needs to calculate commissions across all markets. Volume and commissions must be comparable regardless of where the sale originated.

The question: a distributor in Japan sponsors someone in Germany who buys a product priced in euros. How does this flow through commission calculation?

## The Decision

Three contexts handle currency in a chain.

1. **Commerce** owns regional product catalogs with prices in local currency and pre-assigned CV points.
2. **Network Engine** works exclusively in CV points. It is currency-free.
3. **Financial** converts base-currency commission amounts to payout currency at disbursement.

### Regional Product Catalogs

Companies operating internationally have different product suites per market. This is not just different pricing for the same products. It is often entirely different products. A health device company might sell different models in different countries due to regulatory requirements.

Each product has:
- A market ID (US, EU, JP)
- A price in local currency
- Pre-assigned CV points that are currency-neutral

A product might cost $49.99 in the US and ¥5,500 in Japan. Both carry 40 CV. The CV assignment happens at product configuration time, not at purchase time. There is no runtime currency conversion for volume.

### CV as Currency-Neutral Unit

CV points decouple volume from currency. When Commerce reports volume from an order, it sends CV points.

```go
type VolumeSourceItem struct {
    ProductID string
    Quantity  int
    CVPoints  float64
}
```

The engine routes CV points to tree structures, aggregates them for rank qualification, and uses them in commission calculations. Commission amounts come out in the company's base currency.

### The Payout Chain

Commission results are in base currency. Financial converts to each distributor's payout currency at disbursement time using exchange rates current at the time of payout.

## Why This Approach

**Why not have the engine understand currencies?** It would create dependencies on Financial for exchange rates and on Commerce for market information. CV points break these dependencies. The engine is a pure calculation machine that works on numbers, not money.

**Why pre-assign CV at configuration time?** The alternative is computing CV from purchase price using exchange rates. That means the same product has different commissionable value depending on when it was purchased and what the exchange rate was that day. Pre-assigned CV is deterministic. From our experience with the legacy system 75% of companies in practice treat CV as a fixed point value rather than a currency-derived percentage.

**Why different product suites per market?** This matches real-world practice. Companies in multiple markets typically have different products per region. Treating each market's catalog as independent simplifies the model.

**What about cross-market volume aggregation?** In our experience, companies do not aggregate volume across markets using exchange rates. A distributor's volume in Japan and their volume in the US are both in CV. They aggregate naturally without conversion.

## Currency Responsibility by Context

| Context | Responsibility |
|---------|---------------|
| **Commerce** | Regional product catalogs. Prices in local currency. CV pre-assigned. |
| **Network Engine** | Currency-free. CV points for volume. Base currency for commissions. |
| **Financial** | Base-to-payout currency conversion at disbursement. Exchange rates. |
| **Identity** | International addresses. Phone numbers in E.164 format. User market and locale. |
| **Engagement** | Locale-aware message content. |
| **Operations** | Reports in base currency with optional local breakdown. |

## What We Considered

**Currency-aware engine.** The engine handles all conversion internally. This embeds financial logic in the performance-critical path and creates dependencies on Financial for exchange rates.

**Single global catalog with regional pricing.** One product with price overrides per market. Does not match reality. Companies have genuinely different product suites per region.

**CV derived from purchase price.** Calculate CV as a percentage of the purchase amount converted via exchange rate. Makes CV volatile. The same product would have different commissionable value on different days.

## What This Enables

- **Simple engine.** Zero currency concerns in Network Engine. All volume is numbers. All commissions are in one currency.
- **No runtime conversion for volume.** No exchange rate dependencies in the hot path.
- **Financial handles the complexity.** Currency conversion, exchange rates, payout preferences, and disbursement regulatory requirements all live in one place.
- **International addresses.** Identity uses `Region` instead of `State`, ISO 3166-1 alpha-2 country codes driving validation, and a `Meta` map for country-specific address fields. Phone numbers use E.164 format.
