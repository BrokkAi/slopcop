// Code in the design system's four inks: keywords pen blue, strings ledger green, numbers manila,
// comments quiet. Everything else is the text colour. One theme per ground; the values are the
// --sc-code-* tokens in src/styles/tokens.css, written out because a syntax theme needs plain colours.

const inks = ({ name, type, text, quiet, well, key, str, num }) => ({
  name,
  type,
  colors: {
    'editor.background': well,
    'editor.foreground': text,
  },
  tokenColors: [
    { settings: { foreground: text } },
    { scope: ['comment', 'punctuation.definition.comment'], settings: { foreground: quiet } },
    {
      scope: ['keyword', 'storage', 'constant.language', 'variable.language'],
      settings: { foreground: key },
    },
    // Operators and punctuation are not words: they stay the text colour.
    {
      scope: ['keyword.operator', 'punctuation', 'meta.brace', 'storage.type.function.arrow'],
      settings: { foreground: text },
    },
    // A string is something in quotes. A bare word in YAML or a shell argument is not one.
    {
      scope: [
        'string.quoted',
        'string.template',
        'string.regexp',
        'string.json',
        'punctuation.definition.string',
        'punctuation.support.type.property-name',
      ],
      settings: { foreground: str },
    },
    { scope: ['constant.numeric'], settings: { foreground: num } },
  ],
});

export const slopcopField = inks({
  name: 'slopcop-field',
  type: 'dark',
  text: '#ffffff',
  quiet: '#a6a7a9',
  well: '#0f1114',
  key: '#a9c0ff',
  str: '#9fd8b6',
  num: '#e8c684',
});

export const slopcopPaper = inks({
  name: 'slopcop-paper',
  type: 'light',
  text: '#15171c',
  quiet: '#606165',
  well: '#f7f7f7',
  key: '#2a4b9b',
  str: '#1d6a43',
  num: '#8a5600',
});
