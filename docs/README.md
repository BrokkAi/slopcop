# SlopCop documentation site

The public site at <https://docs.slopcop.com> is an Astro Starlight project.
From this directory:

```sh
npm ci
npm run dev          # http://localhost:4321
npm run check        # astro check
npm run build        # renders dist/ and checks every internal link
PUBLIC_DOCS_BASE=/slopcop npm run build   # proves links hold below a path prefix
```

Pages live under `src/content/docs/`, grouped by section; the sidebar is in
`src/sidebar.mjs`. Link between pages with root-relative paths and a trailing
slash (`/checks/exit-codes/`).

The build also writes the site for language models and agents, after
<https://llmstxt.org>: `/llms.txt` (an index by sidebar group), `/llms-full.txt`
(every page in one file), and a Markdown version of each page at its address
with `.md` (`/get-started/quickstart.md`). `src/llms.ts` makes all three from
the pages' sources.

`.github/workflows/docs.yml` builds and deploys the site to GitHub Pages on a
docs change to `master`, on a `v*.*.*` tag, on a `deploy-docs`
`repository_dispatch`, and on demand. A pull request that touches `docs/` only
builds. The page [This documentation](src/content/docs/contribute/docs.md)
has the details.

The theme is in `src/styles/`: `tokens.css` holds every colour, face and size,
and `slopcop.css` maps them onto Starlight. `rehype-reading.mjs` keeps a flag such
as `--base` on one line and gives each table cell its column's heading, which
the stylesheet uses to set a table as rows on a phone. The fonts under `public/fonts/` are
the ones slopcop.com uses (Redaction 20, Overused Grotesk, Commit Mono), each
under the SIL Open Font License (see the license file beside each).
