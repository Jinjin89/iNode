package inode

import _ "embed"

//go:embed docs/tutorial.md
var tutorial string

func Help() string { return tutorial }
