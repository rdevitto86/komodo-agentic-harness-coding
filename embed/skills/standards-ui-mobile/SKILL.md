---
v: 1
name: standards-ui-mobile
description: "Mobile interface standards for Android and iOS"
globs:
  - "*.kt"
  - "*.swift"
markers:
  - "AndroidManifest.xml"
  - "*.xcodeproj"
sections:
  - "Layout"
  - "Accessibility"
  - "Lifecycle"
---
## Layout
- **Always** size touch targets to at least 44 by 44 points.
- **Never** hard-code a screen size.

## Accessibility
- **Always** give every control a text label for screen readers.
- **Never** carry meaning in color alone.

## Lifecycle
- **Always** release listeners and observers when the screen goes away.
- **Never** do network or disk work on the main thread.
