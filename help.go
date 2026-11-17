package inode

import _ "embed"

//go:embed docs/tutorial.md
var tutorial string

//go:embed skills/inode/SKILL.md
var skill string

func Help() string { return tutorial }

// Skill is the guide an AI agent follows to build with files and the command line.
func Skill() string { return skill }
