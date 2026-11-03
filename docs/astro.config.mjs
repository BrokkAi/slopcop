import { unified } from '@astrojs/markdown-remark';
import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';
import rehypeBasePathLinks from './rehype-base-path-links.mjs';

// The deploy workflow sets PUBLIC_DOCS_SITE and PUBLIC_DOCS_BASE. A local
// `astro dev` always serves from `/`.
const site = process.env.PUBLIC_DOCS_SITE ?? 'https://docs.slopcop.com';
const productionBase = process.env.PUBLIC_DOCS_BASE ?? '/';
const isDev = process.argv.includes('dev');
const base = isDev ? '/' : productionBase;
const socialCardPath = [productionBase.replace(/^\/+|\/+$/g, ''), 'og.png'].filter(Boolean).join('/');
const socialCardUrl = new URL(`/${socialCardPath}`, site).href;
const socialCardAlt =
  'SlopCop documentation: static analysis deep enough for CI, fast enough for your coding agent to run on every edit.';

export default defineConfig({
  site,
  base,
  markdown: {
    processor: unified({
      rehypePlugins: [[rehypeBasePathLinks, { base }]],
    }),
  },
  integrations: [
    starlight({
      title: 'SlopCop',
      description:
        'Documentation for SlopCop: the slopcop CLI, coding-agent setup, the hosted check service, and the web app.',
      head: [
        { tag: 'meta', attrs: { property: 'og:type', content: 'website' } },
        { tag: 'meta', attrs: { property: 'og:image', content: socialCardUrl } },
        { tag: 'meta', attrs: { property: 'og:image:type', content: 'image/png' } },
        { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
        { tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
        { tag: 'meta', attrs: { property: 'og:image:alt', content: socialCardAlt } },
        { tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: socialCardUrl } },
        { tag: 'meta', attrs: { name: 'twitter:image:alt', content: socialCardAlt } },
      ],
      customCss: ['./src/styles/slopcop.css'],
      expressiveCode: {
        styleOverrides: {
          borderRadius: '0',
          borderColor: 'var(--sc-line)',
          borderWidth: '1px',
          codeFontFamily: 'var(--sc-font-mono)',
          codeBackground: 'var(--sc-well)',
          frames: {
            shadowColor: 'transparent',
            terminalTitlebarBackground: 'var(--sc-surface)',
            terminalTitlebarBorderBottomColor: 'var(--sc-line-soft)',
            terminalTitlebarDotsForeground: 'var(--sc-line-strong)',
            terminalTitlebarDotsOpacity: '1',
            editorTabBarBackground: 'var(--sc-surface)',
            editorActiveTabBackground: 'var(--sc-well)',
            editorActiveTabIndicatorTopColor: 'var(--sc-accent)',
            editorTabBarBorderBottomColor: 'var(--sc-line-soft)',
            tooltipSuccessBackground: 'var(--sc-accent-dim)',
            tooltipSuccessForeground: 'var(--sc-accent-text)',
          },
        },
      },
      components: {
        Header: './src/components/SlopCopHeader.astro',
        Hero: './src/components/SlopCopHero.astro',
        ThemeSelect: './src/components/SlopCopThemeSelect.astro',
      },
      logo: {
        src: './src/assets/helmet.svg',
        alt: 'SlopCop',
      },
      favicon: '/favicon.svg',
      editLink: {
        baseUrl: 'https://github.com/BrokkAi/slopcop/edit/master/docs/',
      },
      lastUpdated: true,
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/BrokkAi/slopcop' },
        { icon: 'discord', label: 'Discord', href: 'https://discord.gg/KVk8n5sENg' },
      ],
      sidebar: [
        {
          label: 'Get started',
          items: [
            { label: 'What is SlopCop?', slug: 'get-started/overview' },
            { label: 'Quickstart', slug: 'get-started/quickstart' },
            { label: 'Install the CLI', slug: 'get-started/install' },
            { label: 'What a check finds', slug: 'get-started/what-a-check-finds' },
          ],
        },
        {
          label: 'Coding agents',
          items: [
            { label: 'SlopCop in your agent', slug: 'agents/overview' },
            { label: 'Claude Code', slug: 'agents/claude-code' },
            { label: 'Codex', slug: 'agents/codex' },
            { label: 'Other agents', slug: 'agents/other-agents' },
            { label: 'The agent skill', slug: 'agents/skill' },
            { label: 'The Stop hook', slug: 'agents/stop-hook' },
          ],
        },
        {
          label: 'Checks',
          items: [
            { label: 'Run a check', slug: 'checks/run-a-check' },
            { label: 'Read a report', slug: 'checks/read-a-report' },
            { label: 'Exit codes', slug: 'checks/exit-codes' },
            { label: 'Output formats', slug: 'checks/output-formats' },
            { label: 'False positives', slug: 'checks/false-positives' },
            { label: 'Repository rules', slug: 'checks/repository-rules' },
            { label: 'Gate a change in CI', slug: 'checks/ci' },
          ],
        },
        {
          label: 'Account and billing',
          items: [
            { label: 'Sign in', slug: 'account/sign-in' },
            { label: 'Workspaces', slug: 'account/workspaces' },
            { label: 'Repositories', slug: 'account/repositories' },
            { label: 'Private repositories', slug: 'account/private-repositories' },
            { label: 'Credits and what it costs', slug: 'account/credits' },
            { label: 'The web app', slug: 'account/web-app' },
            { label: 'What leaves your machine', slug: 'account/privacy' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'CLI reference', slug: 'reference/cli' },
            { label: 'Configuration', slug: 'reference/configuration' },
            { label: 'Error codes', slug: 'reference/error-codes' },
            { label: 'HTTP API', slug: 'reference/api' },
          ],
        },
        {
          label: 'Contribute',
          items: [
            { label: 'Development', slug: 'contribute/development' },
            { label: 'Releasing', slug: 'contribute/releasing' },
            { label: 'Publishing to npm', slug: 'contribute/npm-publishing' },
            { label: 'Publishing to PyPI', slug: 'contribute/pypi-publishing' },
            { label: 'This documentation', slug: 'contribute/docs' },
          ],
        },
      ],
    }),
  ],
});
