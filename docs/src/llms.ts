// What the site publishes for a language model or an agent to read (https://llmstxt.org):
//
//   /llms.txt        an index: what SlopCop is, then one line for each page, by sidebar group
//   /llms-full.txt   every page's Markdown in one file, in sidebar order
//   /<page>.md       one page as Markdown, e.g. /get-started/quickstart.md (the home page is /index.md)
//
// The endpoints in src/pages/ call into this file. A page's Markdown is made from its source, not from
// its HTML: a .md page is already Markdown, and a .mdx page has its imports taken out and its
// components written as plain Markdown. A component this file does not know fails the build, so JSX
// never reaches a reader.
import { getCollection, type CollectionEntry } from 'astro:content';
import { sidebar } from './sidebar.mjs';

/** The summary under the title of llms.txt and llms-full.txt. Facts the pages state, in the present tense. */
const summary =
  'SlopCop is hosted static analysis for the changes a coding agent makes. The open-source `slopcop` CLI builds one git patch from the base commit to the working tree and sends it to the hosted check service at slopcop.com. The service applies the patch to its own clone of the repository, runs the Bifrost static analysis engine, and reports each finding the change introduced with its rule, severity, certainty, file and line. A coding agent runs `slopcop` after an edit, fixes what it reports, and checks again; the same command gates a change in CI.';

/** Sidebar groups that llms.txt lists under "Optional": pages about building SlopCop, not using it. */
const optionalGroups = new Set(['Contribute']);

/** Pages outside this site, in the closing section of llms.txt. */
const productLinks = [
  { title: 'slopcop.com', url: 'https://slopcop.com/llms.txt', note: "The product site's own llms.txt." },
  { title: 'Pricing', url: 'https://slopcop.com/pricing', note: 'What a check costs.' },
];

// ---- Addresses. Both honour PUBLIC_DOCS_SITE and PUBLIC_DOCS_BASE through Astro's `site` and `base`. ----

const base = import.meta.env.BASE_URL.replace(/\/+$/, '');

/** A path on this site as the browser asks for it: the deployment base, then the path. */
export function sitePath(path: string): string {
  return `${base}/${path.replace(/^\/+/, '')}`;
}

/** The same path as a whole address. */
export function siteUrl(path: string): string {
  return new URL(sitePath(path), import.meta.env.SITE).href;
}

/** Where a page's Markdown version is served, below the site root: `get-started/quickstart.md`. */
export function markdownPathFor(id: string): string {
  return `${id}.md`;
}

// ---- Pages ----

type DocsEntry = CollectionEntry<'docs'>;

export interface Page {
  /** The collection id: `get-started/quickstart`, and `index` for the home page. */
  id: string;
  title: string;
  description: string;
  /** The address of the HTML page. */
  url: string;
  /** The address of the Markdown version. */
  markdownUrl: string;
  /** The page as Markdown, under its title. */
  markdown: string;
}

interface Group {
  label: string;
  pages: Page[];
}

let loaded: Promise<Map<string, Page>> | undefined;

/** Every page of the site, by id. */
export function allPages(): Promise<Map<string, Page>> {
  loaded ??= loadPages();
  return loaded;
}

async function loadPages(): Promise<Map<string, Page>> {
  const entries = (await getCollection('docs')).filter(
    (entry: DocsEntry) => entry.id !== '404' && !(import.meta.env.PROD && entry.data.draft),
  );
  const ids = new Set(entries.map((entry: DocsEntry) => entry.id));
  const pages = new Map<string, Page>();
  for (const entry of entries) {
    const where = entry.filePath ?? entry.id;
    const pagePath = entry.id === 'index' ? '/' : `/${entry.id}/`;
    const resolve = (href: string) => resolveHref(href, pagePath, ids, where);
    const tagline = entry.data.hero?.tagline;
    const body = toMarkdown(entry.body ?? '', { mdx: where.endsWith('.mdx'), resolve, where });
    pages.set(entry.id, {
      id: entry.id,
      title: entry.data.title,
      description: (entry.data.description ?? '').replace(/\s+/g, ' ').trim(),
      url: siteUrl(pagePath),
      markdownUrl: siteUrl(markdownPathFor(entry.id)),
      markdown: [`# ${entry.data.title}`, tagline ? inlineHtml(tagline, resolve) : '', body]
        .filter(Boolean)
        .join('\n\n'),
    });
  }
  return pages;
}

/** The sidebar's groups with their pages, in the sidebar's order. */
async function sidebarGroups(): Promise<Group[]> {
  const pages = await allPages();
  return sidebar.map((group) => {
    if (typeof group.label !== 'string' || !Array.isArray(group.items)) {
      throw new Error('llms.txt reads a sidebar of labelled groups; teach src/llms.ts any other shape.');
    }
    return {
      label: group.label,
      pages: group.items.map((item) => {
        const page = typeof item.slug === 'string' ? pages.get(item.slug) : undefined;
        if (!page) {
          throw new Error(`llms.txt: the sidebar item ${JSON.stringify(item)} in "${group.label}" names no page.`);
        }
        return page;
      }),
    };
  });
}

// ---- The three files ----

const listLine = (title: string, url: string, note: string) => `- [${title}](${url})${note ? `: ${note}` : ''}`;

/** /llms.txt */
export async function llmsIndex(): Promise<string> {
  const groups = await sidebarGroups();
  const section = (heading: string, lines: string[]) => `## ${heading}\n\n${lines.join('\n')}`;
  const pageLines = (group: Group) => group.pages.map((page) => listLine(page.title, page.markdownUrl, page.description));
  const optional = groups.filter((group) => optionalGroups.has(group.label));
  const parts = [
    '# SlopCop documentation',
    `> ${summary}`,
    `Each link below is the Markdown version of a documentation page. The HTML page is at the same address without \`.md\` and with a trailing slash. [llms-full.txt](${siteUrl('llms-full.txt')}) has every page in one file.`,
    ...groups.filter((group) => !optionalGroups.has(group.label)).map((group) => section(group.label, pageLines(group))),
    section(
      'Product site',
      productLinks.map((link) => listLine(link.title, link.url, link.note)),
    ),
  ];
  if (optional.length > 0) parts.push(section('Optional', optional.flatMap(pageLines)));
  return `${parts.join('\n\n')}\n`;
}

/** /llms-full.txt */
export async function llmsFull(): Promise<string> {
  const groups = await sidebarGroups();
  const parts = [
    [
      '# SlopCop documentation',
      `> ${summary}`,
      `This file has every page of ${siteUrl('/')} as Markdown, in the order of the site's sidebar. Each page starts with its title and its address. [llms.txt](${siteUrl('llms.txt')}) is the index, with one line for each page.`,
    ].join('\n\n'),
  ];
  for (const group of groups) {
    for (const page of group.pages) {
      const [title, ...rest] = page.markdown.split('\n');
      parts.push([title, '', `Source: ${page.url}`, ...rest].join('\n'));
    }
  }
  return `${parts.join('\n\n---\n\n')}\n`;
}

// ---- A page's source as plain Markdown ----

interface Options {
  /** MDX: take out imports, and write components and the home page's HTML as Markdown. */
  mdx: boolean;
  /** Turns a link's target into the address a reader of the Markdown follows. */
  resolve: (href: string) => string;
  /** The file, for an error message. */
  where: string;
}

/** A component or a `:::` aside that is open, and the lines inside it so far. */
interface Frame {
  name: string;
  attrs: Record<string, string>;
  indent: string;
  directive: boolean;
  lines: string[];
}

const asideLabels: Record<string, string> = { note: 'Note', tip: 'Tip', caution: 'Caution', danger: 'Danger' };
const wrappers = new Set(['Steps', 'Tabs', 'CardGrid']);
const labelled = new Set(['TabItem', 'Card']);
const linkCards = new Set(['LinkCard', 'SlopCopNavCard']);
const htmlWrapper = /^<\/?(section|div|aside|ol|ul|li|nav|header|footer|main|article)(\s[^>]*)?>$/;
const htmlBlock = /^<(h[1-6]|p|small|a)(\s[^>]*)?>(.*)<\/\1>$/;
const componentOpen = /^<([A-Z][A-Za-z0-9]*)((?:\s+[A-Za-z][\w-]*(?:=(?:"[^"]*"|'[^']*'|\{[^}]*\}))?)*)\s*(\/?)>$/;
const componentClose = /^<\/([A-Z][A-Za-z0-9]*)>$/;
const directiveOpen = /^:::(note|tip|caution|danger)(?:\[(.*)\])?$/;
const importLine = /^import\s+(?:[^'"]+\s+from\s+)?['"][^'"]+['"];?\s*$/;

export function toMarkdown(source: string, { mdx, resolve, where }: Options): string {
  const root: Frame = { name: '', attrs: {}, indent: '', directive: false, lines: [] };
  const stack: Frame[] = [root];
  let fence: { char: string; length: number } | undefined;
  // How many of the home page's HTML wrappers are open. Inside them indentation is HTML's, not
  // Markdown's, so what they hold is set flush left.
  let htmlDepth = 0;

  for (const line of source.replace(/\r\n?/g, '\n').split('\n')) {
    const top = stack[stack.length - 1]!;
    const text = line.trim();
    const indent = htmlDepth > 0 ? '' : line.slice(0, line.length - line.trimStart().length);

    // Code is copied as it is written.
    if (fence) {
      top.lines.push(line);
      if (new RegExp(`^\\${fence.char}{${fence.length},}$`).test(text)) fence = undefined;
      continue;
    }
    const opening = /^(`{3,}|~{3,})/.exec(text);
    if (opening) {
      fence = { char: opening[1]![0]!, length: opening[1]!.length };
      top.lines.push(line);
      continue;
    }

    const directive = directiveOpen.exec(text);
    if (directive) {
      stack.push({ name: 'Aside', attrs: { type: directive[1]!, title: directive[2] ?? '' }, indent, directive: true, lines: [] });
      continue;
    }
    if (text === ':::' && top.directive) {
      stack.pop();
      stack[stack.length - 1]!.lines.push(...renderFrame(top, resolve));
      continue;
    }

    if (mdx) {
      if (stack.length === 1 && htmlDepth === 0 && importLine.test(line)) continue;

      const open = componentOpen.exec(text);
      if (open) {
        const frame: Frame = { name: open[1]!, attrs: attributes(open[2] ?? ''), indent, directive: false, lines: [] };
        if (!wrappers.has(frame.name) && !labelled.has(frame.name) && !linkCards.has(frame.name) && frame.name !== 'Aside') {
          throw new Error(`${where}: <${frame.name}> has no Markdown form. Add one to src/llms.ts.`);
        }
        if (open[3] === '/') top.lines.push(...renderFrame(frame, resolve));
        else stack.push(frame);
        continue;
      }
      const close = componentClose.exec(text);
      if (close) {
        if (top.name !== close[1] || top.directive) throw new Error(`${where}: </${close[1]}> closes nothing.`);
        stack.pop();
        stack[stack.length - 1]!.lines.push(...renderFrame(top, resolve));
        continue;
      }

      if (htmlWrapper.test(text)) {
        htmlDepth += text.startsWith('</') ? -1 : 1;
        top.lines.push('');
        continue;
      }
      const block = htmlBlock.exec(text);
      if (block) {
        const [, tag, attrs, inner] = block;
        let out = inlineHtml(inner!, resolve);
        if (tag!.startsWith('h')) out = `${'#'.repeat(Number(tag![1]))} ${out}`;
        if (tag === 'a') out = `[${out}](${resolve(attributes(attrs ?? '').href ?? '')})`;
        top.lines.push('', indent + out, '');
        continue;
      }
      if (htmlDepth > 0) {
        top.lines.push(text);
        continue;
      }
    }

    top.lines.push(rewriteLinks(line, resolve));
  }

  if (stack.length > 1) throw new Error(`${where}: <${stack[stack.length - 1]!.name}> is never closed.`);
  const markdown = collapseBlankLines(root.lines).join('\n').trim();
  if (mdx) {
    const left = /<\/?([A-Z][A-Za-z0-9]*)[\s/>]/.exec(outsideCode(markdown));
    if (left) throw new Error(`${where}: <${left[1]}> would reach the Markdown version as JSX. Give it a Markdown form in src/llms.ts.`);
  }
  return markdown;
}

/** What a closed component or aside reads as. */
function renderFrame(frame: Frame, resolve: (href: string) => string): string[] {
  const { name, attrs, indent } = frame;
  if (linkCards.has(name)) {
    return [`${indent}${listLine(attrs.title ?? '', resolve(attrs.href ?? ''), attrs.description ?? '')}`];
  }
  const content = reindent(trimBlankLines(frame.lines), indent);
  // Steps hold an ordered list and tabs hold their items: the tags go and what they hold stays.
  if (wrappers.has(name)) return ['', ...content, ''];
  // A tab or a card: its label in bold, then what it holds.
  if (labelled.has(name)) return ['', `${indent}**${attrs.label ?? attrs.title ?? ''}**`, '', ...content, ''];
  // An aside: a quotation that opens with its kind and its title.
  const label = asideLabels[attrs.type ?? 'note'] ?? 'Note';
  const heading = attrs.title ? `${label}: ${attrs.title}` : label;
  const quoted = content.map((line) => (line === '' ? `${indent}>` : `${indent}> ${line.slice(indent.length)}`));
  return ['', `${indent}> **${heading}**`, `${indent}>`, ...quoted, ''];
}

/** `type="tip" title="One command"` as a record. */
function attributes(source: string): Record<string, string> {
  const attrs: Record<string, string> = {};
  for (const match of source.matchAll(/([A-Za-z][\w-]*)(?:=(?:"([^"]*)"|'([^']*)'|\{([^}]*)\}))?/g)) {
    attrs[match[1]!] = decodeEntities(match[2] ?? match[3] ?? match[4] ?? '');
  }
  return attrs;
}

/** Moves a block so that its least indented line starts at `indent`. */
function reindent(lines: string[], indent: string): string[] {
  const depths = lines.filter((line) => line.trim() !== '').map((line) => line.length - line.trimStart().length);
  const least = depths.length > 0 ? Math.min(...depths) : 0;
  return lines.map((line) => (line.trim() === '' ? '' : indent + line.slice(least)));
}

function trimBlankLines(lines: string[]): string[] {
  let start = 0;
  let end = lines.length;
  while (start < end && lines[start]!.trim() === '') start += 1;
  while (end > start && lines[end - 1]!.trim() === '') end -= 1;
  return lines.slice(start, end);
}

/** One blank line between blocks, and code left alone. */
function collapseBlankLines(lines: string[]): string[] {
  const out: string[] = [];
  let fence: string | undefined;
  for (const line of lines) {
    const text = line.trim();
    const marker = /^(`{3,}|~{3,})/.exec(text)?.[1];
    if (fence) {
      if (marker && marker[0] === fence[0] && marker.length >= fence.length && text === marker) fence = undefined;
    } else if (marker) {
      fence = marker;
    } else if (text === '' && (out.length === 0 || out[out.length - 1]!.trim() === '')) {
      continue;
    }
    out.push(text === '' && !fence ? '' : line);
  }
  return out;
}

/** The text with fenced code and code spans taken out, to look for what should not be left in prose. */
function outsideCode(markdown: string): string {
  return markdown.replace(/^\s*(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\s*\1[`~]*\s*$/gm, '').replace(/`[^`\n]*`/g, '');
}

/** Rewrites the targets of the Markdown links on a line, outside code spans. */
function rewriteLinks(line: string, resolve: (href: string) => string): string {
  return line
    .split(/(`[^`]*`)/)
    .map((part, index) =>
      index % 2 === 1
        ? part
        : part.replace(/\]\(([^)\s]+)(\s+"[^"]*")?\)/g, (_, href: string, title = '') => `](${resolve(href)}${title})`),
    )
    .join('');
}

/** The few inline HTML tags the home page uses, as Markdown. */
function inlineHtml(html: string, resolve: (href: string) => string): string {
  return decodeEntities(
    html
      .replace(/<a\s[^>]*?href="([^"]*)"[^>]*>(.*?)<\/a>/g, (_, href: string, text: string) => `[${text}](${resolve(href)})`)
      .replace(/<code>(.*?)<\/code>/g, '`$1`')
      .replace(/<\/?(strong|b)>/g, '**')
      .replace(/<\/?(em|i)>/g, '*')
      .replace(/<\/?span(\s[^>]*)?>/g, ''),
  );
}

function decodeEntities(text: string): string {
  return text
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&nbsp;/g, ' ')
    .replace(/&amp;/g, '&');
}

/**
 * Where a link in a page leads for a reader of the Markdown. A link to another page of this site
 * becomes the address of that page's Markdown version; a link to a page that does not exist fails
 * the build. A link elsewhere, or to a heading of the same page, stays as written.
 */
function resolveHref(href: string, pagePath: string, ids: Set<string>, where: string): string {
  if (href === '' || href.startsWith('#') || href.startsWith('//') || /^[a-z][a-z\d+.-]*:/i.test(href)) return href;
  const target = new URL(href, `https://docs.invalid${pagePath}`);
  const id = decodeURIComponent(target.pathname).replace(/^\/+|\/+$/g, '') || 'index';
  if (ids.has(id)) return siteUrl(markdownPathFor(id)) + target.hash;
  if (target.pathname.endsWith('/')) throw new Error(`${where}: the link ${href} names no page.`);
  return siteUrl(target.pathname) + target.search + target.hash;
}
