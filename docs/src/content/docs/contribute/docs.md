---
title: This documentation
description: "The docs site lives in the repository under docs/, builds with Astro Starlight, and deploys to docs.slopcop.com from GitHub Pages. How to edit, preview, and release it."
sidebar:
  order: 5
---

This site is an [Astro Starlight](https://starlight.astro.build) project in the
[`docs/`](https://github.com/BrokkAi/slopcop/tree/master/docs) directory of
the `slopcop` repository, so it lives next to the code it describes and changes
in the same pull requests. Every page has an **Edit page** link.

## Edit a page

Pages are Markdown (or MDX, for pages that use Starlight components) under
`docs/src/content/docs/`, grouped by section. Each page starts with
frontmatter:

```md
---
title: Run a check
description: One sentence for search results and link previews.
sidebar:
  order: 1
---
```

The sidebar is declared in `docs/astro.config.mjs`; add a new page there so it
appears. Link between pages with root-relative paths and a trailing slash
(`/checks/exit-codes/`); the build rewrites them for the deployment base and
fails on a link to a page that does not exist.

Write in the repository's voice: plain sentences, the exact command or code
name, and no claim the CLI or service does not make. The README and the agent
skill are the source of truth for CLI behavior; keep them and this site in
step.

## Preview locally

```console
cd docs
npm ci
npm run dev        # http://localhost:4321
npm run build      # renders to docs/dist and checks every internal link
npm run check      # astro check
```

Node 22 or newer. `PUBLIC_DOCS_BASE=/slopcop npm run build` proves the links
also hold when the site is served below a path prefix.

The social card is rendered from `src/assets/social-card.svg` into
`public/og.png` by `npm run social-card`; rerun it after changing the card.

## Deploy

The **Docs** workflow (`.github/workflows/docs.yml`) builds the site and
deploys it to GitHub Pages, which serves it at
[docs.slopcop.com](https://docs.slopcop.com). It runs:

- on a push to `master` that touches `docs/` or the workflow;
- on a `v*.*.*` tag, so the site is redeployed with each CLI release;
- on a `repository_dispatch` with event type `deploy-docs`, which the service's
  own release can send so the site redeploys when the hosted side changes:

  ```console
  gh api repos/BrokkAi/slopcop/dispatches -f event_type=deploy-docs
  ```

- on demand, from the Actions tab.

A pull request that touches `docs/` builds the site without deploying it, so a
broken link or page fails the check before it merges.

Two repository variables override the build target without a code change:
`DOCS_SITE` (default `https://docs.slopcop.com`) and `DOCS_BASE` (default `/`).
The custom domain itself is set in the repository's Pages settings; a
GitHub Actions deployment needs no `CNAME` file.
