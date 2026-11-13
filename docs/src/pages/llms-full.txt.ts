import type { APIRoute } from 'astro';
import { llmsFull } from '../llms';

// /llms-full.txt: every page as Markdown in one file. See src/llms.ts.
export const GET: APIRoute = async () =>
  new Response(await llmsFull(), { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
