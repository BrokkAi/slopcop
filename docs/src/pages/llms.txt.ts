import type { APIRoute } from 'astro';
import { llmsIndex } from '../llms';

// /llms.txt: the index of the documentation for a language model or an agent. See src/llms.ts.
export const GET: APIRoute = async () =>
  new Response(await llmsIndex(), { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
