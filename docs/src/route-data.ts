import { defineRouteMiddleware } from '@astrojs/starlight/route-data';
import { markdownPathFor, sitePath } from './llms';

// The home page's headline is its largest paint and the only use of the display face, so that page
// alone preloads it. (The text face is preloaded on every page, in astro.config.mjs.)
const displayFace = `${import.meta.env.BASE_URL.replace(/\/+$/, '')}/fonts/Redaction_20-Regular.woff2`;

export const onRequest = defineRouteMiddleware((context) => {
  const route = context.locals.starlightRoute;
  // Starlight always links `favicon` as a "shortcut icon". The tab icons in astro.config.mjs pick their ink by
  // colour scheme, and a third icon with no media query would compete with them.
  route.head = route.head.filter((entry) => !(entry.tag === 'link' && entry.attrs?.rel === 'shortcut icon'));
  // Every page names its Markdown version and the llms.txt that covers it, the two link relations
  // llmstxt.org asks for, so an agent that lands on a page finds both without guessing.
  if (route.entry.id !== '404') {
    route.head.push(
      {
        tag: 'link',
        // Starlight gives the home page an empty id; its Markdown version is /index.md.
        attrs: { rel: 'alternate', type: 'text/markdown', href: sitePath(markdownPathFor(route.entry.id || 'index')) },
        content: '',
      },
      { tag: 'link', attrs: { rel: 'describedby', href: sitePath('llms.txt') }, content: '' },
    );
  }
  if (route.entry.data.template !== 'splash' || !route.entry.data.hero) return;
  route.head.push({
    tag: 'link',
    attrs: { rel: 'preload', href: displayFace, as: 'font', type: 'font/woff2', crossorigin: true },
    content: '',
  });
});
