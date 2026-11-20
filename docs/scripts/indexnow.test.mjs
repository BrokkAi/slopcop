// Checks for scripts/indexnow.mjs. They never contact the network.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { ENDPOINT, KEY, KEY_FILE, locs, parseArgs, submit } from './indexnow.mjs';

const SITE = 'https://docs.slopcop.com';
const INDEX = `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
<sitemap><loc>${SITE}/sitemap-0.xml</loc></sitemap>
<sitemap><loc>https://elsewhere.example/sitemap.xml</loc></sitemap>
</sitemapindex>`;
const SITEMAP = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
<url><loc>${SITE}/</loc></url>
<url><loc> ${SITE}/checks/?a=1&amp;b=2 </loc></url>
<url><loc>${SITE}/</loc></url>
<url><loc>https://slopcop.com/</loc></url>
</urlset>`;

function fakeFetch(pages, post = { status: 200 }) {
  const requests = [];
  const fetchImpl = async (url, init = {}) => {
    requests.push({ url, ...init });
    if (init.method === 'POST') return new Response('', post);
    if (!(url in pages)) return new Response('not found', { status: 404 });
    return new Response(pages[url]);
  };
  return { fetchImpl, requests };
}

const PAGES = {
  [`${SITE}/${KEY}.txt`]: `${KEY}\n`,
  [`${SITE}/sitemap-index.xml`]: INDEX,
  [`${SITE}/sitemap-0.xml`]: SITEMAP,
};

test('the key file holds only the key', () => {
  assert.equal(readFileSync(KEY_FILE, 'utf8').trim(), KEY);
  assert.match(KEY, /^[0-9a-f]{32}$/);
});

test('locs reads and unescapes every loc', () => {
  assert.deepEqual(locs(SITEMAP), [`${SITE}/`, `${SITE}/checks/?a=1&b=2`, `${SITE}/`, 'https://slopcop.com/']);
});

test('submits each page on the host once', async () => {
  const { fetchImpl, requests } = fakeFetch(PAGES);
  const lines = [];
  await submit({ site: `${SITE}/`, fetchImpl, log: (line) => lines.push(line) });
  assert.ok(!requests.some((request) => request.url.startsWith('https://elsewhere.example')));
  const post = requests.at(-1);
  assert.equal(post.method, 'POST');
  assert.equal(post.url, ENDPOINT);
  assert.deepEqual(JSON.parse(post.body), {
    host: 'docs.slopcop.com',
    key: KEY,
    keyLocation: `${SITE}/${KEY}.txt`,
    urlList: [`${SITE}/`, `${SITE}/checks/?a=1&b=2`],
  });
  assert.deepEqual(lines, ['IndexNow answered 200 for 2 URLs on docs.slopcop.com']);
});

test('a dry run sends nothing', async () => {
  const { fetchImpl, requests } = fakeFetch(PAGES);
  const body = await submit({ site: SITE, dryRun: true, fetchImpl, log: () => {} });
  assert.equal(body.urlList.length, 2);
  assert.ok(!requests.some((request) => request.method === 'POST'));
});

test('refuses when the site does not serve the key', async () => {
  for (const key of [undefined, 'something else']) {
    const pages = { ...PAGES, [`${SITE}/${KEY}.txt`]: key };
    if (key === undefined) delete pages[`${SITE}/${KEY}.txt`];
    const { fetchImpl, requests } = fakeFetch(pages);
    await assert.rejects(submit({ site: SITE, fetchImpl, log: () => {} }), /key/);
    assert.ok(!requests.some((request) => request.method === 'POST'));
  }
});

test('refuses an empty sitemap and more than 10,000 URLs', async () => {
  const empty = fakeFetch({ ...PAGES, [`${SITE}/sitemap-0.xml`]: '<urlset></urlset>' });
  await assert.rejects(submit({ site: SITE, fetchImpl: empty.fetchImpl, log: () => {} }), /no pages/);
  const many = Array.from({ length: 10_001 }, (_, i) => `<url><loc>${SITE}/p${i}/</loc></url>`).join('');
  const big = fakeFetch({ ...PAGES, [`${SITE}/sitemap-0.xml`]: `<urlset>${many}</urlset>` });
  await assert.rejects(submit({ site: SITE, fetchImpl: big.fetchImpl, log: () => {} }), /over IndexNow/);
  assert.ok(!big.requests.some((request) => request.method === 'POST'));
});

test('fails when IndexNow rejects the request', async () => {
  const { fetchImpl } = fakeFetch(PAGES, { status: 403 });
  await assert.rejects(submit({ site: SITE, fetchImpl, log: () => {} }), /answered 403/);
});

test('parses its arguments', () => {
  assert.deepEqual(parseArgs([]), { site: SITE, dryRun: false });
  assert.deepEqual(parseArgs(['--site', 'https://x.test', '--dry-run']), { site: 'https://x.test', dryRun: true });
  assert.deepEqual(parseArgs(['--site=https://y.test']), { site: 'https://y.test', dryRun: false });
  assert.throws(() => parseArgs(['--nope']), /unknown argument/);
});
