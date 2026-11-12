import { unified } from '@astrojs/markdown-remark';
import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';
import rehypeBasePathLinks from './rehype-base-path-links.mjs';
import rehypeReading from './rehype-reading.mjs';
import { slopcopField, slopcopPaper } from './src/code-themes.mjs';
import { sidebar } from './src/sidebar.mjs';

// The deploy workflow sets PUBLIC_DOCS_SITE and PUBLIC_DOCS_BASE. A local
// `astro dev` always serves from `/`.
const site = process.env.PUBLIC_DOCS_SITE ?? 'https://docs.slopcop.com';
const productionBase = process.env.PUBLIC_DOCS_BASE ?? '/';
const isDev = process.argv.includes('dev');
const base = isDev ? '/' : productionBase;
const socialCardPath = [productionBase.replace(/^\/+|\/+$/g, ''), 'og.png'].filter(Boolean).join('/');
const socialCardUrl = new URL(`/${socialCardPath}`, site).href;
// The text face is on every page, so every page preloads its regular weight.
const textFaceUrl = `/${[base.replace(/^\/+|\/+$/g, ''), 'fonts/OverusedGrotesk-400.woff2'].filter(Boolean).join('/')}`;
const socialCardAlt =
  'SlopCop documentation. A detective on a ladder takes a box from tall shelves of evidence boxes under a hanging lamp.';

export default defineConfig({
  site,
  base,
  markdown: {
    processor: unified({
      rehypePlugins: [[rehypeBasePathLinks, { base }], rehypeReading],
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
        {
          tag: 'link',
          attrs: { rel: 'preload', href: textFaceUrl, as: 'font', type: 'font/woff2', crossorigin: true },
        },
      ],
      customCss: ['./src/styles/tokens.css', './src/styles/slopcop.css'],
      routeMiddleware: './src/route-data.ts',
      // Code sits in a well with no frame, in the mono face at 13 on 21, in four inks.
      expressiveCode: {
        themes: [slopcopField, slopcopPaper],
        minSyntaxHighlightingColorContrast: 0,
        styleOverrides: {
          borderRadius: '0',
          borderColor: 'transparent',
          borderWidth: '0px',
          codeFontFamily: 'var(--sc-mono)',
          codeFontSize: 'var(--sc-t-mono)',
          codeLineHeight: '21px',
          codePaddingBlock: 'var(--sc-4)',
          codePaddingInline: 'var(--sc-5)',
          codeBackground: 'var(--sc-well)',
          codeForeground: 'var(--sc-fg)',
          uiFontFamily: 'var(--sc-sans)',
          uiFontSize: 'var(--sc-ui-small)',
          focusBorder: 'var(--sc-fg)',
          frames: {
            shadowColor: 'transparent',
            frameBoxShadowCssValue: 'none',
            editorBackground: 'var(--sc-well)',
            editorTabBarBackground: 'var(--sc-well)',
            editorTabBarBorderColor: 'transparent',
            editorTabBarBorderBottomColor: 'var(--sc-hair)',
            editorActiveTabBackground: 'var(--sc-well)',
            editorActiveTabForeground: 'var(--sc-quiet)',
            editorActiveTabBorderColor: 'transparent',
            editorActiveTabIndicatorTopColor: 'transparent',
            editorActiveTabIndicatorBottomColor: 'transparent',
            terminalBackground: 'var(--sc-well)',
            terminalTitlebarBackground: 'var(--sc-well)',
            terminalTitlebarForeground: 'var(--sc-quiet)',
            terminalTitlebarBorderBottomColor: 'var(--sc-hair)',
            terminalTitlebarDotsOpacity: '0',
            inlineButtonBackground: 'var(--sc-fg)',
            inlineButtonForeground: 'var(--sc-fg)',
            inlineButtonBorder: 'var(--sc-fg)',
            tooltipSuccessBackground: 'var(--sc-fg)',
            tooltipSuccessForeground: 'var(--sc-bg)',
          },
        },
      },
      components: {
        Header: './src/components/SlopCopHeader.astro',
        Hero: './src/components/SlopCopHero.astro',
        ThemeSelect: './src/components/SlopCopThemeSelect.astro',
      },
      logo: {
        light: './src/assets/gumshoe-light.svg',
        dark: './src/assets/gumshoe-dark.svg',
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
      sidebar,
    }),
  ],
});
