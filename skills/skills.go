// Package skills carries the agent skill the CLI installs.
package skills

import "embed"

// Files is the skill tree: slopcop/SKILL.md and its harness metadata.
//
//go:embed slopcop
var Files embed.FS
