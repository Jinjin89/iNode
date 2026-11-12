# iNode

A local, self-hosted workflow tool with a browser editor and a command line.

## Install

Needs Go 1.27 or newer.

```sh
git clone https://github.com/Jinjin89/iNode && cd iNode
go build -o inode ./cmd/inode
```

Put the `inode` binary somewhere on your `PATH`. The built frontend is included; `./build.sh` rebuilds both frontend and binary.

## Learn

Any folder is a project. Start with a copy of the examples:

```sh
cp -r examples ~/my-flows && cd ~/my-flows
inode start
```

Follow [the one-page tutorial](docs/tutorial.md) in the browser's **Guide** or with `inode help`: connect steps, run them, save your own node, put it in a box and schedule it. It also shows the workflow and program-node file forms.

For AI agents: [the iNode skill](skills/inode/SKILL.md).

## Everyday commands

The [tutorial](docs/tutorial.md#the-same-from-the-command-line) covers finding, checking, running and scheduling nodes. For existing runs and secrets:

| Command | What it does |
| --- | --- |
| `inode runs again <id>` | runs it again from the start |
| `inode runs from <id> <step>` | previews running from one step, reusing earlier results; add `--start` to start it |
| `inode secret set <name>` | stores a secret; workflows refer to it by name |

## More

- [docs/design.md](docs/design.md): the model, types, control nodes and limits.
- [docs/architecture.md](docs/architecture.md): how it is built.
- [examples/](examples): one small workflow for each feature.
