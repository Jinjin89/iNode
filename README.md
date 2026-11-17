# iNode

A local, self-hosted workflow tool with a browser editor and a command line.

## At a glance

What you see of iNode, and what sits beneath it. The deeper the level, the fewer people need its words.

```text
                                     /\
                                    /  \
                                   /    \
                                  /      \
                                 /        \
                                /  iNode   \
                               /            \
                              /   workflow   \                                  what you see
                             /                \
                            /                  \
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~    the waterline
                         /                        \
                       /    node · run · trigger    \                           1  words you learn
                     /--------------------------------\
                   /                                    \
                 /  step · box · input · output · plugin  \                     2  parts you handle
               /--------------------------------------------\
             /                                                \
           / types · checker · no cycles · run record · secrets \               3  rules that protect you
         /--------------------------------------------------------\
       /                                                            \
     /  catalog · runner · child runs · queue · plugin host · store   \         4  machinery that runs it
    /------------------------------------------------------------------\
     \                                                                /
       \     Node · Port · Record · processes · JSON · SHA-256      /           5  code and disk
          \                                                      /
           '-.                                                .-'
              '--..                                      ..--'
                   ''--..__                      __..--''
                           ''---....____....---''
```

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

For AI agents: `inode skill` prints [the guide](skills/inode/SKILL.md) an agent follows to build, run and schedule workflows from files and the command line, so the page is only for viewing. Tell your agent to run it, or install it where the agent looks for skills, for example for Claude Code:

```sh
mkdir -p ~/.claude/skills/inode && inode skill > ~/.claude/skills/inode/SKILL.md
```

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
