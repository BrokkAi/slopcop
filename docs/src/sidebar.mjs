// The sidebar: the site's groups and their order. astro.config.mjs gives it to Starlight, and
// src/llms.ts reads the same list to order llms.txt and llms-full.txt.
export const sidebar = [
  {
    label: 'Get started',
    items: [
      { label: 'What is SlopCop?', slug: 'get-started/overview' },
      { label: 'Quickstart', slug: 'get-started/quickstart' },
      { label: 'Install the CLI', slug: 'get-started/install' },
      { label: 'What a check finds', slug: 'get-started/what-a-check-finds' },
    ],
  },
  {
    label: 'Coding agents',
    items: [
      { label: 'SlopCop in your agent', slug: 'agents/overview' },
      { label: 'Claude Code', slug: 'agents/claude-code' },
      { label: 'Codex', slug: 'agents/codex' },
      { label: 'OpenCode and Kilo Code', slug: 'agents/opencode' },
      { label: 'Pi', slug: 'agents/pi' },
      { label: 'GitHub Copilot CLI', slug: 'agents/copilot-cli' },
      { label: 'Grok and Kimi Code', slug: 'agents/grok-kimi' },
      { label: 'Other agents', slug: 'agents/other-agents' },
      { label: 'Lovable, Bolt, and MCP', slug: 'agents/app-builders' },
      { label: 'The agent skill', slug: 'agents/skill' },
      { label: 'The Stop hook', slug: 'agents/stop-hook' },
      { label: 'Managed rollout', slug: 'agents/managed-rollout' },
    ],
  },
  {
    label: 'Checks',
    items: [
      { label: 'Run a check', slug: 'checks/run-a-check' },
      { label: 'Read a report', slug: 'checks/read-a-report' },
      { label: 'Exit codes', slug: 'checks/exit-codes' },
      { label: 'Output formats', slug: 'checks/output-formats' },
      { label: 'False positives', slug: 'checks/false-positives' },
      { label: 'Repository rules', slug: 'checks/repository-rules' },
      { label: 'Gate a change in CI', slug: 'checks/ci' },
      { label: 'Check pull requests', slug: 'checks/pull-requests' },
    ],
  },
  {
    label: 'Account and billing',
    items: [
      { label: 'Sign in', slug: 'account/sign-in' },
      { label: 'Workspaces', slug: 'account/workspaces' },
      { label: 'Repositories', slug: 'account/repositories' },
      { label: 'Private repositories', slug: 'account/private-repositories' },
      { label: 'Credits and what it costs', slug: 'account/credits' },
      { label: 'The web app', slug: 'account/web-app' },
      { label: 'What leaves your machine', slug: 'account/privacy' },
    ],
  },
  {
    label: 'Reference',
    items: [
      { label: 'CLI reference', slug: 'reference/cli' },
      { label: 'Configuration', slug: 'reference/configuration' },
      { label: 'Error codes', slug: 'reference/error-codes' },
      { label: 'HTTP API', slug: 'reference/api' },
    ],
  },
  {
    label: 'Contribute',
    items: [
      { label: 'Development', slug: 'contribute/development' },
      { label: 'Releasing', slug: 'contribute/releasing' },
      { label: 'Publishing to npm', slug: 'contribute/npm-publishing' },
      { label: 'Publishing to PyPI', slug: 'contribute/pypi-publishing' },
      { label: 'This documentation', slug: 'contribute/docs' },
    ],
  },
];
