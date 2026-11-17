---
name: inode
description: Build, run, debug and schedule iNode workflows from the command line by writing `.flow` and `.node` files. Use when the user wants something automated with iNode, mentions an iNode workflow, node, run or trigger, or the folder holds `.flow` files.
---

You build with files and the `inode` command; the user watches in the browser. Any folder is a project, and commands act on the current folder, starting its backend when needed.

Everything is a **node**: named typed inputs, one job, named typed outputs. A **workflow** is steps (uses of nodes) wired together; saved as `name.flow` in the project folder it is itself the node `name`. A **run** is one execution, recorded step by step. A **trigger** starts runs when something happens.

## Procedure

1. **Find nodes.** `inode nodes` lists name and description; `inode show <node>` gives exact inputs, outputs and types as JSON. Read `show` for every node before using it; do not guess port names.
2. **No node does the job?** Wrap a program as a `.node`, or add a plugin for a service or tool (both below). Computation is always a node: references select data and cannot calculate.
3. **Write `name.flow`.** A new or edited workflow file, or `.node` file in `nodes/`, is live at once; nothing restarts. Trigger and plugin files are applied as described below.
4. **`inode check name`**, fix every problem, then **`inode run name key=value …`**.
5. **On failure**, read `inode runs show <id>`, fix the file and run again. `inode runs follow <id>` watches a run that is still going and `inode runs stop <id>` stops it. To keep finished expensive steps, `inode runs from <id> <step>` previews running that step and the steps that depend on it with the earlier results, and `--start` starts it. It refuses, with the reason, when the step waits `after` an earlier one or an earlier step's node or filled inputs changed. It does not notice a changed program behind an earlier step: after fixing one, run the whole workflow.
6. **Hand over.** `inode start` prints the page address; give the user the whole address, including the part after `#`.

## Workflow file

```yaml
# report.flow
doc: Count the words in several files and note when it finished.
inputs:
  paths: list of text
  log: {type: text, default: report.log}
steps:
  counts: {use: map, in: {over: =inputs.paths, do: count_file}}
  note:   {use: file.write, in: {path: =inputs.log, text: counted}, after: [counts]}
outputs: {counts: =counts.result}
```

- `in` fills inputs with written values or references: `=step.output`, `=inputs.name`, then dots into fields and list items (`=counts.result.0.count`; the run fails if the item is not there). A workflow input is required or has a default. References work inside written lists and objects. `==` writes a literal leading `=`.
- A step runs as soon as what it references is ready, so independent steps run in parallel. `after: [step]` makes a step wait for another whose output it does not use.
- Step and port names contain no dots. Give every workflow a `doc`; it is its description in `inode nodes`.
- The page rewrites the file on save: comments are lost, and a `view` part (canvas positions) may appear. Leave `view` alone.

### Boxes: a node given to another node

`map`, `choose`, `repeat`, `retry`, `try` and `agent` take nodes as inputs and call them. Such an input holds a node name, `{use: name, in: {…}}` with some inputs filled, or a workflow written in place under `use`:

```yaml
# count_file.flow: a file that cannot be read counts as zero.
doc: Count the words in one text file.
inputs: {path: text}
steps:
  count:
    use: try
    in:
      do:
        use:
          inputs: {path: text}
          steps:
            file:  {use: file.read, in: {path: =inputs.path}}
            words: {use: word_count, in: {text: =file.text}}
          outputs: {count: =words.count}
        in: {path: =inputs.path}
      on_error:
        use: {inputs: {error: object}, steps: {}, outputs: {count: 0}}
outputs: {result: {path: =inputs.path, count: =count.result}}
```

- A node in a box has at most one open input (the one its caller fills) and exactly one output. Fill the rest in `in`; to pass or return several values use one object, as `result` does above.
- Steps inside a box see only the box's own `inputs` and steps. Outside values enter through the box's `in`.
- `inode show map` and the others state what each box must accept and give: `repeat`'s `do` returns `{state, done}`, `try`'s `on_error` receives `{step, message}`.
- `retry` accepts only nodes marked `no_outside_effect`.

A loop keeps its state in one value. `repeat` calls `do` with the state, starting from `start`, until the reply says `done`; this is how to page or poll. `choose` calls one of two nodes:

```yaml
# pages.flow
doc: Add one until the number reaches the limit.
inputs: {limit: number}
steps:
  loop:
    use: repeat
    in:
      start: 0
      max: 20
      do:
        use:
          inputs: {n: number, limit: number}
          steps:
            next: {use: number.add, in: {a: =inputs.n, b: 1}}
            more: {use: number.less, in: {a: =next.value, b: =inputs.limit}}
            done: {use: choose, in: {if: =more.value, then: {use: data.value, in: {value: false}}, else: {use: data.value, in: {value: true}}}}
          outputs: {reply: {state: =next.value, done: =done.result}}
        in: {limit: =inputs.limit}
outputs: {n: =loop.result}
```

### An AI step

`agent` gives a model a goal and nodes to call as tools, and returns the model's final text. Every model and tool call is a child run in the record.

```yaml
answer:
  use: agent
  in:
    goal: =inputs.question
    model: {use: my_model, in: {key: model_key}}
    tools: [word_count]
    max: 5
```

- The model calls tools without asking, outside effects included: list only tools the user accepts it using freely.
- A tool is a node with exactly one open plain-data input and one output. Wrap a node with none or several in a workflow that takes one input.
- No model node is built in. `model` is any node with one open `object` input and one `object{text: text, calls: list of object}` output, usually a program node that calls a provider's API with the key as a `secret` input.
- It receives `{messages, tools}`. A message is `{role, text}` with role `user`, `assistant` or `tool`; an assistant message carries its `calls`, a tool message the `id` of the call it answers. A tool is `{name, description, parameters}`, `parameters` being a JSON Schema of its input.
- It returns `{text, calls}`, each call `{id, name, input}` with `name` exactly as given in `tools`. A reply with no calls ends the step and its `text` is the result; `max` model calls without one fails.

## Program node

`nodes/<name>.node` turns a program into the node `<name>`. No shell: one program with arguments, so no pipes, redirects or `$VAR`.

```yaml
# nodes/word_count.node
doc: Count the words in a text.
inputs: {text: text}
outputs: {count: number}
no_outside_effect: true
run: {args: [wc, -w], stdin: =inputs.text, stdout: count}
```

An input is required, has a default (`{type: number, default: 0}`) or is optional (`{type: text, optional: true}`). With only `run: python3 script.py` (or an argument list), the program reads all inputs as one JSON object on stdin and prints all outputs as one JSON object. Bindings change that:

| Binding | What it does |
|---|---|
| `args: [..., =inputs.x]` | One argument per scalar or list item. |
| `{flag: --name, value: =inputs.x}` inside `args` | True sends the flag, false omits it; a scalar or list repeats flag and value. |
| `stdin: =inputs.x` | File bytes or text unchanged; otherwise JSON. |
| `stdout: count` | That one output: file bytes, text unchanged, otherwise one JSON value. |
| `env: {TOKEN: =inputs.token}` | Scalars; a secret can only go here or to stdin. |
| `files: {in.md: =inputs.source, out.html: =outputs.html}` | Stages input files and collects output files under these names, used in `args`, in a fresh private folder. Without a `stdout` binding, stdout then carries a JSON object of the other outputs only, and is ignored when there are none. |

- A binding of an unset optional input is omitted. A nonzero exit fails the run with the program's stderr.
- Without `files`, the program runs in `nodes/`, so a script beside the `.node` is found by its name, and a relative path in an input is not relative to the project: use `file` inputs or absolute paths. With `files`, it runs in the private folder, so give a script by absolute path.
- Set `no_outside_effect: true` only when the program publishes nothing and changes no user or service data; it may read, compute and cost money.
- Types: `text`, `number`, `bool`, `any`, `list of T`, `object{name: T}`, `file`, `secret`. A `file` input accepts a path; `file.save` copies a returned file to disk.

## More nodes

A **plugin** is a set of nodes named `<plugin>.<node>`. It is trusted code running with the user's permissions, so first show the user what `inode plugins inspect <source>` reports (name, `accesses`, program) and install only when they agree.

| Source | Becomes |
|---|---|
| An OpenAPI 3 document, file or URL | One node per API operation, returning `{status, body, files}`; an HTTP error fails the run. |
| An MCP server command, such as `npx -y @modelcontextprotocol/server-filesystem ~/notes` | One node per tool. |
| A plugin folder or git URL | Its nodes. |

`inode add <source>` asks its questions on a terminal. Without one, write the answers as one JSON object: `preparation.input` from the inspection, plus any field listed in `preparation.editable`. Inspect that same object, show the user the result, then add it:

```sh
inode plugins inspect api.json
inode plugins inspect '{"kind": "openapi", "source": "api.json", "name": "pets", "base": "https://api.example.com/v1", "env": {"key": "pets_key"}}'
inode add '{"kind": "openapi", "source": "api.json", "name": "pets", "base": "https://api.example.com/v1", "env": {"key": "pets_key"}}'
inode add '{"kind": "command", "source": "npx -y some-mcp-server", "name": "notes"}'
inode add tools        # a plugin folder has no questions
inode plugins          # name, version, state, source, and the reason when not running
```

- `env` binds each variable the source asks for to the name of a stored secret.
- An OpenAPI or MCP source is written to a folder `<name>/`, which is then its source. Installing copies the source into `.inode/` and lists it in `plugins`.
- `inode add <source>`, with the source `inode plugins` shows, installs it again; that is also the fix for `unavailable` or `missing`. `inode remove <name>` removes it.

To keep several program nodes together, write the folder yourself:

```yaml
# tools/plugin; nodes/shout.node beside it becomes tools.shout
name: tools
version: "1"
doc: Small text tools.
accesses: [Reads nothing outside its inputs.]
setup: python3 setup.py      # optional, run once in the installed folder
```

After editing the folder run `inode add tools` again.

## What commands print

| Command | Standard output | Otherwise |
|---|---|---|
| `inode run <node> k=v …` | the outputs as JSON | `run <id>: <state>` on stderr; a failure exits 1 with the failed step and message |
| `inode check <workflow>` | JSON; `problems` is empty, `unchecked` lists connections only verified while running | problems exit 1, one line each naming step, input and fix |
| `inode show <node>` | JSON description; a workflow also carries its definition | |
| `inode runs` | one line per run, newest first: id, node, state, what started it | |
| `inode runs show <id>` | JSON record: `state`, `inputs`, `outputs`, `error`, and `steps` | |

- In a record each step has `state` (`waiting`, `running`, `done`, `failed`, `interrupted`), `inputs` (what it received), `outputs`, `error`, and its own `steps` for the calls a box made. The failed step is the one to fix; steps left `waiting` never started.
- `key=value` values for `text`, `secret` and `file` inputs (a path) are taken as typed; any other input is YAML, so quote lists, objects and boxes for the shell: `'paths=[a.txt, b.txt]'`, `'do={use: text.join, in: {separator: "+"}}'`.
- Run a single node directly to test it: `inode run word_count text='a b c'`. `inode help` ends with every command.

## Triggers

`inode trigger add <kind> key=value … run=<node> key=value …` writes `triggers/<name>.flow` and prints the name. Kinds are nodes (`schedule.every`, `schedule.daily`, `folder.new_file`; `inode show` them): the settings are the kind's inputs, and its outputs are the event, used as `=event.<output>`:

```sh
inode trigger add folder.new_file path=inbox run=count_file path==event.path
inode trigger on folder.new_file-1
inode trigger list        # state, current run, waiting events, last run, last failure
```

A trigger starts **off**. `inode trigger off|clear|remove <name>` switch it off, drop its waiting events, delete it. After editing a trigger's file, `inode trigger on <name>` applies it, also when it is already on. Runs of one trigger never overlap, and a failed run does not stop it. Triggers fire only while the backend runs: `inode stop` stops them all.

## Acting for the user

- **Secrets.** Never read, ask for or type a secret value. A `secret` input takes the name of a stored secret; `inode secret list` shows names. If one is missing, ask the user to run `inode secret set <name>` themselves.
- **Outside effects.** Running a workflow really sends, posts and writes. Test steps with outside effects on harmless inputs, or ask first.
- **Triggers and the backend.** Switch a trigger on only when the user wants it running unattended, and say that it is on. Do not `inode stop` a project whose triggers are on unless asked.

## Limits that change how you build

| You want | Do this |
|---|---|
| Ports that depend on a value | One node per mode; data of unknown shape travels in one `object` or `list`. |
| Cleanup after failure | `try` with `on_error`; there is no "finally". |
| Streaming between steps | A node returns once; split the data and `map`. |
