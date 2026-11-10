// Package skills carries the agent skills the CLI installs.
package skills

import "embed"

// Files is the skill tree: one folder per skill, each with its SKILL.md and
// harness metadata. slopcop checks a change; slopcop-audit audits landed code.
//
//go:embed slopcop slopcop-audit
var Files embed.FS
