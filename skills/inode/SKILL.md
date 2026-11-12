---
name: inode
description: Build or edit iNode workflows and program nodes, and diagnose their runs, using the tool's own descriptions and checker.
---

A node takes named inputs, does one job and returns named outputs. A saved workflow is a node that other workflows can use.

Ask the tool; do not guess: `inode help` gives the tutorial and file forms, `inode nodes` lists available nodes, and `inode show <node>` describes one.

Find suitable nodes, write or edit a `.flow` or `.node`, then use `inode check` and `inode run`. Read `inode runs show <id>` to see what each step received and returned; fix the problem and repeat.

`inode show`, successful `inode check`, `inode run` and `inode runs show` return JSON an agent can read.
`inode nodes` is a readable listing; `inode help` is text.

Commands that need a project start a background backend for the current folder when needed; it keeps running, with switched-on triggers active, until `inode stop`.
