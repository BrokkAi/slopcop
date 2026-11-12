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

The sidebar is declared in `docs/src/sidebar.mjs`; add a new page there so it
appears. Link between pages with root-relative paths and a trailing slash
(`/checks/exit-codes/`); the build rewrites them for the deployment base and
fails on a link to a page that does not exist.

Write in the repository's voice: plain sentences, the exact command or code
name, and no claim the CLI or service does not make. The README and the agent
skill are the source of truth for CLI behavior; keep them and this site in
step.

## Markdown versions and llms.txt

The build writes the site a second time for language models and agents, in
the form [llmstxt.org](https://llmstxt.org) describes:

| Address | What it is |
| --- | --- |
| `/llms.txt` | An index: what SlopCop is, then one line for each page under its sidebar group, with the page's `description`. The Contribute group is listed under `Optional`. |
| `/llms-full.txt` | Every page in the sidebar as Markdown in one file, in the sidebar's order. |
| `/<page>.md` | One page as Markdown, such as `/get-started/quickstart.md`. The home page is `/index.md`. |

Every HTML page names its Markdown version with
`<link rel="alternate" type="text/markdown">` and the index with
`<link rel="describedby">`.

`docs/src/llms.ts` makes all three from the pages' sources, so there is
nothing to keep in step by hand: a page needs a `description` and a place in
the sidebar. A link to another page becomes the address of that page's
Markdown version. In an MDX page the imports are taken out and each component
is written as plain Markdown: an aside as a quotation, a tab as its label in
bold, steps as their list. A component `llms.ts` does not know fails the
build; give it a Markdown form there.

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

The social card is the picture `public/og.png`, 1200 by 630 pixels. Its alt
text is `socialCardAlt` in `astro.config.mjs`; change both together.

## Deploy

The **Docs** workflow (`.github/workflows/docs.yml`) builds the site and
deploys it to GitHub Pages, which serves it at
[docs.slopcop.com](https://docs.slopcop.com). It deploys only from the
`docs-production` branch, so a page goes live with the release it describes, not
when its pull request merges.

`docs-production` stays an ancestor of `master` and only ever fast-forwards. Two
releases move it:

- a stable CLI release: the **Release** workflow fast-forwards `docs-production` to
  the tagged commit and runs the Docs workflow there (prerelease tags such as
  `v0.2.0-rc.1` leave it alone);
- a promotion of the hosted service to production: bifrost-service's
  **Promote to production** fast-forwards `docs-production` to `master`.

A push to `master` or a pull request that touches `docs/` builds the site
without deploying it, so a broken link or page fails the check before it
merges. Running the workflow from the Actions tab on `docs-production` redeploys
the current site; on any other branch it only builds.

So merge docs to `master` once what they describe is ready to ship with the
next release:

- CLI docs merge with the CLI change and go live with the next CLI release.
- Hosted-feature docs merge once the service change is on bifrost-service
  `main`, and go live at the next promotion to production.

Two repository variables override the build target without a code change:
`DOCS_SITE` (default `https://docs.slopcop.com`) and `DOCS_BASE` (default `/`).
The custom domain itself is set in the repository's Pages settings; a
GitHub Actions deployment needs no `CNAME` file.
