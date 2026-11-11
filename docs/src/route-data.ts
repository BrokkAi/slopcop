import { defineRouteMiddleware } from '@astrojs/starlight/route-data';

// The home page's headline is its largest paint and the only use of the display face, so that page
// alone preloads it. (The text face is preloaded on every page, in astro.config.mjs.)
const displayFace = `${import.meta.env.BASE_URL.replace(/\/+$/, '')}/fonts/Redaction_20-Regular.woff2`;

export const onRequest = defineRouteMiddleware((context) => {
  const route = context.locals.starlightRoute;
  if (route.entry.data.template !== 'splash' || !route.entry.data.hero) return;
  route.head.push({
    tag: 'link',
    attrs: { rel: 'preload', href: displayFace, as: 'font', type: 'font/woff2', crossorigin: true },
    content: '',
  });
});
