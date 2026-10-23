// Package skills carries the agent skill the CLI installs.
package skills

import "embed"

// Files is the skill tree: slopcop/SKILL.md and its harness metadata.
//
//go:embed slopcop
var Files embed.FS

// Previous lists the SHA-256 of every earlier release's copy of a skill file,
// by its slash-separated path inside the skill folder. slopcop init replaces
// an installed skill whose files each match this release's copy or one of
// these, so an untouched skill picks up new guidance, and leaves a skill with
// local changes alone. When a release changes a skill file, add the digest
// of the copy it replaces here.
var Previous = map[string][]string{
	"SKILL.md": {
		"b81c9461e0bee46ce2a5739643d02d795259a5e61bec70df5974717d6046a86e", // v0.0.1 to v0.0.3
		"d33ef48f0876ffa4fc37b0de9dd452808007de40b545d604cfbefe2403e3b086", // master: new findings only
		"66d7338f54f7463db0b63303649d27d06f2a15eb937f94ab29325768fd532c63", // master: fail-on finding
		"d1700486a79987c6b086d7689edecf7ce437386a483b59180649544c31f64a6c", // v0.0.4
		"7b26de8de195d6784d47465569044ec7e8b0c01ef9fb28a3225f35631d203505", // #18: --since, rules and categories
		"acd4a6fabbcd678674efe0101c046057b3c208fa66dbe261c4d33ac9831cf66d", // #18: fail-on info
	},
}
