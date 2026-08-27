# Design

The visual language for every surface this project grows: the web app, the
desktop app, and any page a future agent adds. Read this before writing UI;
change it only deliberately, and then everywhere at once.

## The idea

The interface is a dense, quiet page of information for people who are
willing to act on it. Typography and spacing do all the work; color is
reserved for meaning (interactive, danger, success) and never used as
decoration. If a screen looks like a marketing site, it is wrong; if it looks
like a well-set reference page, it is right.

- No gradients, no shadows, no rounded cards floating on tinted backgrounds.
- No animation except a 120ms ease on hover/focus states.
- No icon fonts or icon libraries; short words beat pictograms.
- Empty states are one muted sentence, not an illustration.
- Every screen must be leavable: any modal or overlay gets a visible close
  control, always, even mid-operation.

## Tokens

Both themes come from one set of custom properties. Components reference
tokens, never raw colors. Dark mode follows `prefers-color-scheme` and must
never be a separate stylesheet.

```css
:root {
  --bg: #ffffff;      /* page */
  --fg: #16181d;      /* text */
  --muted: #69707d;   /* secondary text */
  --line: #e3e5e8;    /* borders, rules */
  --accent: #2563eb;  /* links, focus */
  --danger: #b42318;
  --ok: #067647;
  --radius: 6px;
  --mono: ui-monospace, "SF Mono", SFMono-Regular, Menlo, Consolas, monospace;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #101114; --fg: #e8eaed; --muted: #8f96a3; --line: #26282d;
    --accent: #60a5fa; --danger: #f97066; --ok: #47cd89;
  }
}
```

## Rules

- **Type**: the system UI stack at 15px/1.55. One `h1` per page (1.15rem,
  600). Section headings are small caps-style labels: 0.8rem, uppercase,
  letter-spaced, `--muted`. Identifiers, hashes, uids, filenames are always
  `--mono`.
- **Layout**: one centered content column capped at 72rem, 8px spacing
  grid. On small screens, wide tables scroll inside themselves; the page
  never scrolls horizontally. Whitespace separates
  sections; rules (`1px solid var(--line)`) separate rows.
- **Buttons**: 1px `--line` outline, transparent fill, `--radius`. The one
  primary action per screen inverts (`--fg` fill, `--bg` text). Destructive
  actions stay outlined and turn `--danger` on hover, and never sit where a
  primary is expected.
- **Forms**: same outline treatment. Labels above inputs, `--muted`,
  0.82rem. Placeholder text is an example, never the label.
- **Tables**: full-width, no zebra striping, `--line` bottom rules, headers
  in `--muted` 0.82rem. Numbers right-aligned.
- **Links**: `--accent`, no underline until hover.
- **Status**: plain words in `--ok`/`--danger`/`--muted` ("printing",
  "failed", "idle"), no badges, no pills.

The canonical implementation of these tokens ships in this repo's stylesheet;
the identity service's page uses the same set. If a token changes here,
change it there in the same sitting.
