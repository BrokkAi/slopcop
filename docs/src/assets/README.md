# SlopCop branding

The Gumshoe mark and the icons are slopcop.com's: a hat with a red band over two lapels, drawn on a
48-pixel grid. They are built by `client/scripts/logo-icons.py` in `bifrost-service` (run it with
`--docs` pointing at this folder's parent), which writes `gumshoe-light.svg`, `gumshoe-dark.svg`,
`public/favicon-light.png`, `public/favicon-dark.png`, `public/favicon.ico`, `public/icon-192.png` and
`public/apple-touch-icon.png`, and redraws the mark in `public/og.png`. The mark's ink is the
documentation theme's text colour (night `#15171c` on white, white on night) and the hat band stays
`#ee3c42`; the tab icons are night on a light tab bar and bone on a dark one. Beside the name the mark is
0.96 of the name's font size, so that one pixel of the mark is one pixel of a letter.

The header uses the same Redaction 20 Bold wordmark subset and spacing as
the public landing page. The subset and its SIL Open Font License are in
`public/fonts/Redaction_20-Bold-wordmark.woff2` and `public/fonts/Redaction-OFL.txt`.
The headline of the documentation home page is Redaction 20 Regular, text is
Overused Grotesk, and code is Commit Mono, as on slopcop.com
(`src/styles/tokens.css`).
The social card is the finished picture `public/og.png`; nothing renders it at build time.
