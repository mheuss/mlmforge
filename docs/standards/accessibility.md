# Accessibility and Localization

## Accessibility

**Target standard:** WCAG 2.1 AA

### Principles

- **Perceivable.** Content available to all senses (alt text, captions, sufficient contrast)
- **Operable.** All functionality available via keyboard. No time traps
- **Understandable.** Clear language, predictable behavior, helpful error messages
- **Robust.** Works with assistive technologies. Valid, semantic markup

### Project Guidelines

- **Genealogy tree visualizations** must have accessible alternatives (tabular data view, screen reader-compatible navigation)
- **Commission dashboards** must present financial data in screen-reader-friendly formats with proper ARIA labels
- **Forms** (enrollment, order placement, profile editing) must have proper label associations, error announcements, and keyboard navigation
- **Color alone** must never convey meaning. Always pair with text or icons (e.g., rank status, volume thresholds)
- **Data tables** (reports, commission details) must use proper `<th>` elements with scope attributes
- **Real-time updates** (commission calculations, event bus notifications) must use ARIA live regions
- **Mobile portals** must be operable via touch, keyboard, and voice controls
- **Focus management.** Dynamic content changes (modals, drawers, tab switches) must manage focus appropriately

---

## Localization

### Principles

- All user-facing strings must be externalized
- Avoid concatenating translated strings. Use interpolation
- Support RTL layouts where applicable
- Use locale-aware formatting for dates, numbers, and currency

### Supported Locales

| Locale | Language | Status |
|--------|----------|--------|
| en-US | English (US) | Primary |

Additional locales will be added as the project matures. The architecture must support multi-language from day one (per the legacy system's existing i18n capability).

### Implementation

- **Framework:** React i18n library (react-intl or next-intl) for frontend. Go i18n package for API messages
- **String files:** JSON-based locale files per context
- **Date/number formatting:** Use ICU MessageFormat patterns. Locale-aware number and currency formatting
- **Currency:** Multi-currency support via the Operations context (Currency & Localization domain)
