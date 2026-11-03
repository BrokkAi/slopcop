# SlopCop documentation site

The public site at <https://docs.slopcop.com> is an Astro Starlight project.
From this directory:

```sh
npm ci
npm run dev          # http://localhost:4321
npm run check        # astro check
npm run build        # renders dist/ and checks every internal link
PUBLIC_DOCS_BASE=/slopcop npm run build   # proves links hold below a path prefix
npm run social-card  # re-renders public/og.png from src/assets/social-card.svg
```

Pages live under `src/content/docs/`, grouped by section; the sidebar is in
`astro.config.mjs`. Link between pages with root-relative paths and a trailing
slash (`/checks/exit-codes/`).

`.github/workflows/docs.yml` builds and deploys the site to GitHub Pages on a
docs change to `master`, on a `v*.*.*` tag, on a `deploy-docs`
`repository_dispatch`, and on demand. A pull request that touches `docs/` only
builds. The page [This documentation](src/content/docs/contribute/docs.md)
has the details.

The fonts under `public/fonts/` are the ones slopcop.com uses, each under the
SIL Open Font License (see the license file beside each).
