# quietbox brand assets

Direction "Calm": an inbox tray with a short terracotta line above it (a "mute" sign).
Source canvas: https://claude.ai/artifact/85KgQiu5iwFBLHqy8PEutb

## Files

| File | Use |
|---|---|
| `banner-light.svg` / `banner-dark.svg` | README header (use both with `<picture>`, see below) |
| `banner-light@2x.png` / `banner-dark@2x.png` | PNG fallbacks of the banners (2560×640) |
| `social-preview.png` | GitHub → Settings → Social preview (1280×640) |
| `icon.png` / `icon-dark.png` (+ `.svg`) | GitHub org avatar, app icon (512×512) |
| `logo-mark.svg` / `logo-mark-dark.svg` | The mark alone, transparent background |
| `favicon.svg` | Website favicon; switches colours with the browser's light/dark mode |
| `favicon-32.png` | Fallback favicon |
| `favicon-180.png` | `apple-touch-icon` |
| `favicon-512.png` | Web app manifest icon |
| `src/build.py` | Regenerates everything (text is converted to outlines) |

All text in the SVGs is outlined, so they render identically without the font installed.

## README header

```html
<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/banner-dark.svg">
    <img alt="quietbox: a calmer inbox, on a server you own" src="assets/banner-light.svg" width="640">
  </picture>
</p>
```

## Colours

| Token | Light | Dark | Use |
|---|---|---|---|
| background | `#F4EDE2` (sand) | `#1F1915` (night) | page background |
| text | `#2B211B` (ink) | `#F4EDE2` | headings, body, mark strokes |
| muted | `#6B5A4C` | `#BBA996` | secondary text (both pass WCAG AA on their background) |
| accent | `#C2643F` (clay) | `#E07F57` | the mark's quiet line, decorative accents |
| accent text | `#9E4A2C` | `#E07F57` | links and accent-coloured text |
| line | `#E2D3BE` | `#3A2D24` | dividers, borders |

Use the accent sparingly: the mark has exactly one terracotta element and the brand should feel the same way.

## Type

- **Manrope** (Google Fonts, OFL): 700 for the wordmark and headings, 500 for body and taglines.
- Wordmark: always lowercase `quietbox`, Manrope 700, letter-spacing −0.04em.
- For code and terminal snippets on the website, a monospace such as JetBrains Mono.

## Voice

Tagline: "A calmer inbox, on a server you own."
Descriptor: "Self-hosted inbox triage for any IMAP mailbox."
Calm, plain and specific. No hype, no exclamation marks.

## Regenerating

```sh
# needs: pip install fonttools uharfbuzz resvg-py brotli
# and Manrope-500.ttf / Manrope-700.ttf in a folder
python3 assets/src/build.py path/to/fonts assets/
```
