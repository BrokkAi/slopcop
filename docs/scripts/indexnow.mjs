#!/usr/bin/env node
// Tells IndexNow search engines (Bing, Yandex, Seznam, Naver) that
// docs.slopcop.com changed, so engines that feed agent search (Bing behind
// ChatGPT search, Copilot and DuckDuckGo) recrawl within minutes instead of on
// their own schedule. The Docs workflow runs it after each deploy.
//
// IndexNow keys are public by design: an engine proves the submitter controls
// the host by fetching the key file, public/<KEY>.txt, from that host. The
// script checks the site serves that file before it submits, because an engine
// answers 403 for a key it cannot fetch.
//
//   node scripts/indexnow.mjs [--site https://docs.slopcop.com] [--dry-run]
import { fileURLToPath } from 'node:url';

export const KEY = '990d5785657e8c18ef6e54b18cf28349';
export const KEY_FILE = fileURLToPath(new URL(`../public/${KEY}.txt`, import.meta.url));
export const ENDPOINT = 'https://api.indexnow.org/indexnow';
export const DEFAULT_SITE = 'https://docs.slopcop.com';
// The protocol caps a request at 10,000 URLs.
export const MAX_URLS = 10_000;
const USER_AGENT = 'slopcop-indexnow/1';
const ENTITIES = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'" };

/** The text of every <loc> in a sitemap or sitemap index, in order. */
export function locs(xml) {
  return [...xml.matchAll(/<loc>([\s\S]*?)<\/loc>/g)].map((match) =>
    match[1].trim().replace(/&(amp|lt|gt|quot|apos);/g, (_, name) => ENTITIES[name]),
  );
}

function onHost(url, host) {
  try {
    return new URL(url).hostname === host;
  } catch {
    return false;
  }
}

async function fetchText(url, fetchImpl) {
  const response = await fetchImpl(url, { headers: { 'User-Agent': USER_AGENT } });
  if (!response.ok) throw new Error(`${url} answered ${response.status}`);
  return response.text();
}

/** The page URLs on host that the sitemap index lists through its sitemaps, without duplicates. */
export async function pageUrls(site, host, fetchImpl) {
  const urls = new Set();
  const sitemaps = locs(await fetchText(`${site}/sitemap-index.xml`, fetchImpl)).filter((url) => onHost(url, host));
  for (const sitemap of sitemaps) {
    for (const url of locs(await fetchText(sitemap, fetchImpl))) {
      if (onHost(url, host)) urls.add(url);
    }
  }
  return [...urls];
}

/** Submits the site's sitemap pages to IndexNow once the site serves the key file; returns the request body. */
export async function submit({ site = DEFAULT_SITE, dryRun = false, fetchImpl = fetch, log = console.log } = {}) {
  site = site.replace(/\/+$/, '');
  const host = new URL(site).hostname;
  const keyLocation = `${site}/${KEY}.txt`;
  let served;
  try {
    served = (await fetchText(keyLocation, fetchImpl)).trim();
  } catch (error) {
    throw new Error(`${error.message}; is the deploy with the key file live?`);
  }
  if (served !== KEY) throw new Error(`${keyLocation} does not serve the IndexNow key`);
  const urls = await pageUrls(site, host, fetchImpl);
  if (urls.length === 0) throw new Error(`${site}/sitemap-index.xml lists no pages on ${host}`);
  if (urls.length > MAX_URLS) throw new Error(`${urls.length} URLs is over IndexNow's ${MAX_URLS} per request`);
  const body = { host, key: KEY, keyLocation, urlList: urls };
  if (dryRun) {
    log(JSON.stringify(body, null, 2));
    return body;
  }
  const response = await fetchImpl(ENDPOINT, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8', 'User-Agent': USER_AGENT },
    body: JSON.stringify(body),
  });
  // 200 means accepted, 202 accepted pending the key check; anything else fails.
  if (!response.ok) throw new Error(`IndexNow answered ${response.status}: ${await response.text()}`);
  log(`IndexNow answered ${response.status} for ${urls.length} URLs on ${host}`);
  return body;
}

export function parseArgs(argv) {
  const options = { site: DEFAULT_SITE, dryRun: false };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--dry-run') options.dryRun = true;
    else if (argv[i] === '--site' && i + 1 < argv.length) options.site = argv[++i];
    else if (argv[i].startsWith('--site=')) options.site = argv[i].slice('--site='.length);
    else throw new Error(`unknown argument ${argv[i]}; usage: indexnow.mjs [--site URL] [--dry-run]`);
  }
  return options;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  try {
    await submit(parseArgs(process.argv.slice(2)));
  } catch (error) {
    console.error(`indexnow: ${error.message}`);
    process.exitCode = 1;
  }
}
