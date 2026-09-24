# Routeweft UI Style Contract

Status: **Normative for frontend work**

Reference visual snapshot: LiteRouter main `2ffb7922954112b30425cd487d686758e519397e`.

Routeweft follows this visual language but implements it independently with React + TypeScript + Vite.

## 1. Design statement

**Neutral instrument panel; color carries state, not decoration.**

The UI should feel like an operational console:

- calm;
- dense without being cramped;
- clear hierarchy;
- low visual noise;
- predictable controls;
- readable numeric/status information.

Avoid a generic AI-dashboard look.

Do not use decorative blue/purple gradients, glassmorphism everywhere, oversized marketing cards, or ornamental glow as default control-plane styling.

## 2. Typography

Primary sans:

```text
IBM Plex Sans
fallback: ui-sans-serif, system-ui
```

Monospace:

```text
JetBrains Mono
fallback: ui-monospace, SFMono-Regular, Menlo, Consolas
```

Use monospace for:

- model IDs;
- provider/model route names;
- API-key fragments;
- request IDs;
- technical values where alignment improves scanning.

Material Symbols Outlined is the primary icon vocabulary.

## 3. Core color tokens

### Light

```css
--brand-50:  #f4f4f2;
--brand-100: #e8e8e5;
--brand-200: #d3d3cf;
--brand-300: #b5b5b0;
--brand-400: #8d8d88;
--brand-500: #292a2d;
--brand-600: #202125;
--brand-700: #17181b;
--brand-800: #101114;
--brand-900: #090a0c;

--bg:        #f4f4f1;
--bg-alt:    #e9e9e5;
--surface:   #fbfbf9;
--surface-2: #eeeeeb;
--surface-3: #deded9;
--sidebar:   #ececea;

--border:        #d8d6cd;
--border-subtle: #e5e3db;

--text-main:   #202225;
--text-muted:  #6d6d68;
--text-subtle: #96968f;
```

### Dark

```css
--brand-500: #e9e9e5;
--brand-600: #f4f4f0;

--bg:        #121314;
--bg-alt:    #18191a;
--surface:   #1b1c1e;
--surface-2: #232426;
--surface-3: #303135;
--sidebar:   #1a1b1d;

--border:        #37383b;
--border-subtle: #292a2d;

--text-main:   #f1f0ec;
--text-muted:  #a3a29c;
--text-subtle: #74746f;
```

### Status

```css
--danger-light:  #cf222e;
--danger-dark:   #ef4444;
--success-light: #10B981;
--success-dark:  #22c55e;
--warning-light: #F59E0B;
--warning-dark:  #fbbf24;
--info-light:    #3B82F6;
--info-dark:     #60a5fa;
```

Do not turn status colors into page chrome.

## 4. App shell

Desktop:

- full viewport;
- max outer width: 1920px;
- left sidebar width: 15.5rem;
- main content scrolls independently;
- content max width: 110rem;
- main horizontal padding: 1rem mobile, 1.75rem small desktop, 3rem large desktop;
- vertical workspace padding: roughly 1.75rem mobile / 2.5rem large.

Mobile:

- sidebar becomes drawer;
- dark translucent backdrop;
- touch targets >= 44px where practical;
- no horizontal page overflow.

## 5. Sidebar

Groups:

**Operate**
- Overview
- Endpoint & Key
- Providers

**Route**
- Combo & Capability Adapter
- System One

**Observe**
- Usage
- Quota Tracker
- Token Saver

**System**
- Console Log
- Settings

Style:

- sidebar background uses `--sidebar`;
- subtle right border;
- compact section kickers: ~10px, uppercase, high tracking;
- active row: primary text + ~10% primary background + narrow left active rail;
- inactive row: muted text, subtle hover surface;
- nav row minimum height ~44px;
- icons ~18px;
- brand/control-plane mark ~40px square with 10px radius.

## 6. Page heading

Use a reusable PageIntro equivalent.

Eyebrow:

- 11px;
- uppercase;
- font-weight 700;
- tracking ~0.14em;
- muted/primary-neutral.

Title:

- 30px mobile / 36px larger;
- semibold;
- line-height ~1.05;
- tracking around -0.06em;
- max width around 4xl.

Description:

- 14px;
- line-height ~24px;
- muted;
- max width ~2xl.

Page intro ends with a subtle bottom border and approximately 24px bottom padding.

## 7. Cards

Base card:

- `surface` background;
- 1px/ring subtle border;
- 10px radius;
- minimal soft shadow;
- padding scale: 12 / 16 / 24 / 32px.

Avoid large floating-shadow cards by default.

Elevated card is reserved for:

- modal-like emphasis;
- selected/critical interactive panel;
- meaningful visual layering.

List rows use separators and subtle surface hover.

## 8. Buttons

Variants:

- primary;
- secondary;
- outline;
- ghost;
- danger;
- success.

Primary remains neutral brand, not a bright gradient.

Sizes:

- small: min-height 36px;
- medium: min-height 40px;
- large: ~44px.

Behavior:

- semibold;
- 8–12px corner radius depending on size;
- active press scale around 0.98;
- visible focus outline;
- disabled cursor/opacity;
- spinner/icon ~18px.

## 9. Forms

- labels are concise and close to control;
- inputs use surface/background tokens;
- focus uses primary outline/ring;
- destructive controls are visually separated;
- secrets are masked by default;
- inline validation is preferred over toast-only validation;
- large JSON/raw payload editors use monospace.

## 10. Tables and operational lists

Prefer:

- sticky or clear headers where helpful;
- tabular numeric figures;
- stable alignment;
- subdued row separators;
- explicit empty/loading/error states;
- compact filters;
- pagination instead of unbounded rendering.

Avoid decorative zebra striping unless it demonstrably improves readability.

## 11. Metrics

Metrics use:

- tabular numerals;
- tight negative tracking for large values;
- status color only when value meaning warrants it;
- no arbitrary gradient KPI tiles.

Overview should become useful from one read-model request rather than many sequential browser joins.

## 12. Toasts

Toast palette:

- success: green tint;
- error: red tint;
- warning: warning tint;
- info: blue tint.

Use compact text and clear dismiss behavior.

Toasts do not replace persistent error state for a failed page/resource.

## 13. Theme behavior

Support:

- light;
- dark;
- system.

Theme selection must apply before first meaningful paint to avoid a light/dark flash.

Native select controls must respect dark color scheme.

## 14. Motion

Default transitions: ~150–250ms.

Mobile drawer may use a slower spring-like easing.

Respect `prefers-reduced-motion`.

Avoid continuous decorative animations.

Operational live-state animation should be restrained and only communicate activity.

## 15. Accessibility

Required:

- keyboard navigation;
- visible focus;
- skip-to-content;
- semantic labels for icon-only actions;
- adequate contrast;
- reduced motion;
- `aria-live` for relevant async notifications;
- no information conveyed solely by color.

## 16. UI implementation rules

- create reusable primitives before page-local one-offs;
- tokens are semantic, not page-specific;
- do not hardcode random colors/radii across components;
- prefer server read models that reduce browser fan-out;
- use TanStack Query for server state;
- use local store only for UI state/preferences;
- do not expose provider credentials to browser code;
- do not add a Next runtime.

## 17. Visual acceptance

A new Routeweft page is visually acceptable when it could sit next to the current LiteRouter dashboard without looking like a different design system, while all branding/text identifies Routeweft.

Similarity is intentional at the design-language level; implementation code remains Routeweft-owned.
