import type { APIRoute, GetStaticPaths } from 'astro';
import { allPages } from '../llms';

// The Markdown version of each page, at the page's address with `.md` in place of the closing
// slash: /get-started/quickstart.md, and /index.md for the home page. See src/llms.ts.
export const getStaticPaths: GetStaticPaths = async () =>
  [...(await allPages()).values()].map((page) => ({ params: { page: page.id }, props: { markdown: page.markdown } }));

export const GET: APIRoute = ({ props }) =>
  new Response(`${props.markdown}\n`, { headers: { 'Content-Type': 'text/markdown; charset=utf-8' } });
