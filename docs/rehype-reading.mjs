// Two things about a page's text that a stylesheet cannot know.
//
// 1. A flag is one word. A browser may break a line after any hyphen, so `--add-repository` can end a line
//    as `--`, which reads as the end-of-options marker. Each short hyphenated word of inline code is wrapped
//    in a span the stylesheet keeps on one line. The text itself is unchanged, so it copies as written.
// 2. On a phone a table of three columns is set as rows, and a cell away from its column needs its
//    heading. Each body cell carries its column's heading as `data-label`.

// A longer word may be wider than a phone's column, so it keeps the browser's own breaks.
const LONGEST_WHOLE_WORD = 28;

function text(node) {
  if (node.type === 'text') return node.value;
  return Array.isArray(node.children) ? node.children.map(text).join('') : '';
}

function keepFlagsWhole(code) {
  if (!code.children.every((child) => child.type === 'text')) return;
  code.children = text(code)
    .split(/(\s+)/)
    .filter((part) => part !== '')
    .map((part) =>
      part.includes('-') && part.length <= LONGEST_WHOLE_WORD && !/\s/.test(part)
        ? { type: 'element', tagName: 'span', properties: { className: ['sc-whole'] }, children: [{ type: 'text', value: part }] }
        : { type: 'text', value: part },
    );
}

function elements(node, tagName) {
  return (node.children ?? []).filter((child) => child.type === 'element' && child.tagName === tagName);
}

function labelCells(table) {
  const head = elements(table, 'thead')[0];
  const headings = head ? elements(elements(head, 'tr')[0] ?? { children: [] }, 'th').map((th) => text(th).trim()) : [];
  if (headings.length === 0) return;
  for (const body of elements(table, 'tbody')) {
    for (const row of elements(body, 'tr')) {
      elements(row, 'td').forEach((cell, index) => {
        if (headings[index]) cell.properties = { ...cell.properties, 'data-label': headings[index] };
      });
    }
  }
}

export default function rehypeReading() {
  return (tree) => {
    const stack = [[tree, false]];
    while (stack.length > 0) {
      const [node, inPre] = stack.pop();
      if (!node || typeof node !== 'object') continue;
      if (node.type === 'element' && node.tagName === 'code' && !inPre) keepFlagsWhole(node);
      if (node.type === 'element' && node.tagName === 'table') labelCells(node);
      const nowInPre = inPre || (node.type === 'element' && node.tagName === 'pre');
      if (Array.isArray(node.children)) for (const child of node.children) stack.push([child, nowInPre]);
    }
  };
}
