# iNode

A local, self-hosted workflow tool. You connect **nodes** into a workflow, **run** it, and optionally let a **trigger** run it for you. Everything is a plain file in your project folder, and the browser page and the command line can do the same things.

## Install

Needs Go 1.27 or newer.

```sh
git clone https://github.com/Jinjin89/iNode && cd iNode
go build -o inode ./cmd/inode
```

Put the `inode` binary somewhere on your `PATH`.

## Try it

Any folder is a project. Start with the examples:

```sh
cp -r examples ~/my-flows && cd ~/my-flows
inode run greeting name=cat      # prints {"text": "Hello CAT"}
inode start                      # prints the address of the page; open it in a browser
```

The page shows the same workflows on a canvas, where you can edit, run and inspect them. `inode stop` stops the backend.

## A workflow

A workflow is a `.flow` file in the project folder. Its file name is its name.

```yaml
# greeting.flow
doc: Greet somebody in capitals.
inputs: {name: text}
steps:
  name:     {use: text.upper, in: {text: =inputs.name}}
  greeting: {use: text.join,  in: {parts: [Hello, =name.text]}}
outputs: {text: =greeting.text}
```

- A step uses a node and fills in its inputs.
- A value starting with `=` is a reference: `=inputs.name` is a workflow input, `=name.text` is the `text` output of step `name`. Anything else is a literal.
- Steps run as soon as what they reference is ready. There is no order to write down.
- A saved workflow is itself a node, so other workflows can use it by name.

`inode nodes` lists every node you can use, and `inode show <node>` shows its inputs and outputs. Mistakes are reported before anything runs, with the step and input they belong to.

## Your own node

A node can be any program that reads JSON on standard input and writes JSON on standard output. Describe it in `nodes/<name>.node`:

```yaml
# nodes/count_words.node
doc: Count the words in a text
inputs: {text: text}
outputs: {count: number}
run: python3 count_words.py
```

```python
# nodes/count_words.py
import json, sys
text = json.load(sys.stdin)["text"]
json.dump({"count": len(text.split())}, sys.stdout)
```

A plugin is a folder of such nodes; install one with `inode add <folder or git URL>`.

## Everyday commands

| Command | What it does |
| --- | --- |
| `inode run <node> key=value ...` | runs a node or workflow and prints its outputs |
| `inode runs` | lists past runs; `runs show <id>` shows what every step received and returned |
| `inode runs again <id>` | runs it again from the start |
| `inode runs from <id> <step>` | previews re-running from one step, reusing earlier results; add `--start` to start it |
| `inode trigger add folder.new_file path=inbox run=count_text text==event.name` | runs a workflow for each new file in a folder |
| `inode trigger add schedule.daily at=09:00 run=greeting name=cat` | runs a workflow every day |
| `inode trigger on <name>` / `off <name>` / `list` | triggers start switched off |
| `inode secret set <name>` | stores a secret; workflows refer to it by name and never see it in records |
| `inode help` | everything else |

## More

- [docs/design.md](docs/design.md): the whole model on a few pages, with the control nodes (`map`, `choose`, `repeat`, `retry`, `try`), types and limits.
- [docs/architecture.md](docs/architecture.md): how it is built.
- [examples/](examples): one small workflow for each feature.
