# iNode design

iNode is a local, self-hosted workflow framework inspired by n8n and ComfyUI. This is the conceptual design: what the user sees and what the system promises. How it is built is in `architecture.md`. Names and syntax (`inode`, `.flow`, YAML) are illustrative.

## Aim

Anybody should be able to use iNode with almost nothing to learn first. That gives four tests for every design choice:

- **Nothing to memorise.** Every node describes itself, and the frontend and command line show that description wherever the node is used.
- **Detectable mistakes are prevented.** A connection that cannot work cannot be made. What can only be known while running (a missing field in a reply, an expired credential) is marked as unchecked beforehand and explained in terms of the user's workflow when it fails.
- **Everything is visible.** After a run, each step shows what it received and what it returned.
- **What you see is how it runs.** Order is set by nesting and connections, and both are drawn. There are no hidden execution rules.

The one-page tutorial at the end is the acceptance test. If a concept does not fit on that page, it costs too much.

## Three concepts

| Concept | What it is |
|---|---|
| **Node** | The single shape for every operation: named inputs, one job, named outputs. A workflow is also a node. |
| **Run** | One execution of a node with given inputs. |
| **Trigger** | A standing instruction: when something happens, start a run. |

Two supporting words are not concepts of their own. A **step** is a node placed in a workflow. A **plugin** is a folder of nodes.

## Node

A node has a name (`plugin.name`), a one-sentence description, named typed inputs (each required or with a default), and named typed outputs. When run, it receives all its inputs and either returns all its outputs or fails.

- **No separate configuration.** An input is filled with a value you write or with a reference to another output. A node with some inputs filled in is still a node, with fewer open inputs.
- **Fixed ports.** Inputs and outputs never change with the values given. A capability with several modes becomes several nodes. Data whose shape is only known at run time (query rows, spreadsheet columns) travels inside one `object` or `list` port.
- **No outside effect.** A plugin may mark an operation as having no effect outside the run. Such an operation publishes nothing and does not change user data, an outside service's data, the objects it was given, or state shared with other runs. It may read data that changes, compute, and create new values and new files in the core's store. Using up time, money or a quota is allowed. Taking a message off a queue, moving a shared cursor, or adding to a stored conversation is not. Anything unknown stays unmarked. A workflow has the mark when every step in it does, including what is in its boxes, which the checker works out. The frontend shows it as "can be retried", and says that a retry may cost again and may give a different result.

Types are names, checked when connecting:

| Type | Meaning |
|---|---|
| `text`, `number`, `bool`, `any` | Plain data. |
| `list of T`, `object{name: T, …}` | A list with its item type; an object with named fields. Plain `list` and `object` leave the contents unstated. |
| `file` | Immutable stored bytes, identified by SHA-256, labelled with a name and byte size: `{"$file":"sha256:…","name":"video.mp4","size":1234}`. A file input accepts a path, typed or arriving through an unchecked text connection; bytes are stored on acceptance or arrival. |
| `secret` | The name of a stored secret. |
| `node(T) gives U`, `node() gives U` | A node to be called by the receiver: the type of its one open input, if it has one, and of its single output. |
| plugin-named handles | A live object such as a loaded model. Only nodes of the plugin that created it can operate on it, and it lives as long as that plugin's process. |

- A capital letter in a description (`T`, `U`) stands for "the same type wherever this letter appears" in that one use of the node.
- Where a type is `any` or an unstated `object`, the connection is allowed and shown as unchecked. The value is checked against what the consumer expects before the consumer starts, and a node's outputs are checked against its description when it returns.
- Secret names and handles pass through workflows, boxes and control nodes as opaque references. The core resolves a secret for a non-workflow node's `secret` input or an explicit plugin launch binding; a control node sees only the name. Missing secrets are reported before the node or process starts. Records show input names. Exact secret values in outputs, errors or process diagnostics are replaced before display/recording; encoded or transformed copies are not recognised. Plugins are trusted code and can send values elsewhere.

## Workflow

A workflow is a node made of steps. Each step's inputs are filled by values or by references to other steps (`=page.body`, with dotted paths into data such as `=resp.json.user.name`, where a number picks a list item: `=feed.items.0.title`). `=inputs.url` refers to the workflow's own input. A reference can also sit inside a written list or object. A text that really starts with `=` is written with `==`. Step and port names contain no dots. Any input can be **exposed** as an input of the workflow, and any output as an output of the workflow, which is what makes the workflow a node.

**A node in a `node` input is not a step.** It does not run by itself; the receiving step calls it. In the frontend the input is a box. A box holds a saved node or steps built directly inside it (an inline workflow, the same thing without a file name). Anything inside the box runs when the box's owner calls it.

In a file, a node in a box is written like a step: `{use: …, in: {…}}`, or just its name. `use` is a node's name or a workflow written in place. `in` fills some of its inputs; a reference there is read once per run of the enclosing workflow, before the box's owner starts, and is drawn as entering the box. Steps inside a box see only the box's own inputs and steps, so everything that comes from outside enters this way.

A node passed this way has at most one open input and exactly one output. A node that needs several values takes one object, and one that returns several things exposes one object. The standard control nodes:

| Node | Contract |
|---|---|
| `map(over: list of T, do: node(T) gives U)` | Calls `do` once per item, one at a time. `result: list of U`, in the same order. |
| `choose(if: bool, then: node() gives T, else: node() gives T)` | Calls one of the two. `result: T`. |
| `repeat(start: T, do: node(T) gives object{state: T, done: bool}, max: number)` | Calls `do` with the state until `done`. `result: T`, the final state. Reaching `max` calls without `done` fails. Covers paging and polling. |
| `retry(do: node() gives T, times: number)` | Calls `do` up to `times` times in total until it succeeds. `result: T`. Accepts only nodes with no outside effect. |
| `try(do: node() gives T, on_error: node(error: object) gives T)` | Calls `on_error` with the failed step's name and message when `do` fails. `result: T`. |
| `agent(goal: text, tools: list of node)` | An AI model calls the given nodes as tools; a node's name, description and inputs are the tool definition. `result: text`. Tools must have plain-data open inputs and a plain-data output; secrets, handles and node inputs are filled in beforehand. |

A step can also list other steps in `after`. It then waits for them to succeed without using their outputs. The frontend draws this as its own kind of line. It orders side effects; it does not undo or clean up anything on failure.

## Run

1. A run is one node plus input values. Calling a node from inside another starts a child run.
2. In a workflow run, every step runs once, as soon as everything it references or comes after is ready. Independent steps may run in parallel.
3. There are no cycles, including through saved workflows and boxes. Repetition happens inside `map` and `repeat`.
4. When a step fails, no new steps start in that run, steps already started are left to finish, and then the run fails naming the step. A failed child run is returned to its caller as an error. If the caller does not handle it, the caller fails too.
5. Every run records what each step received and returned, and the workflow as it was. Records are for inspection.

**Run again** always starts a fresh run. Nothing is reused or recovered automatically. If the backend stops during a run, the run is marked interrupted with outcome unknown and is never replayed by itself.

**Run from here** is the one explicit way to reuse past results, for changing a late step without redoing expensive earlier ones. On a step of a finished past run, it starts a fresh run of that step and everything downstream of it, using the workflow as it is saved now; nothing else runs. Every value this part or the workflow's outputs need from elsewhere is copied from the run on screen, labelled with its source and age, and shown before it starts. It is refused, with the reason, when a needed value did not come from a completed step, when an `after` crosses into the selected part, or when a step that supplied a value would be given a different node, different filled-in inputs or a different saved workflow today. A change to the code of a node upstream is not detected. It works within one workflow, not inside one past iteration of a `map`.

## Trigger

A trigger says: when this happens, run this node with these inputs. A kind of "when" is itself a node: its inputs are the trigger's settings and its outputs are the event data. Every day at a time, a web request arriving and a file appearing in a folder are such nodes, and plugins can bring more. A trigger is written as two steps, `event` (the kind) and `run` (the node), so the event data fills the node's inputs with the usual references (`=event.path`). Used as an ordinary step in a workflow, a kind waits for one event and returns it.

Each event starts its own ordinary run. The behaviour is fixed and the same for every trigger:

- Runs of one trigger do not overlap; events wait in order. The trigger shows its current run and how many events are waiting.
- A failed run does not stop the trigger. The failure shows in the run list and on the trigger.
- Times missed while iNode was not running are skipped and reported once.
- An accepted event is a run from that moment: it is stored as a run that is waiting, and the waiting runs of a trigger are its queue. After a restart, a run that had not started still runs once; a run that had started is marked interrupted and is not started again. Events may arrive twice. What happens before acceptance depends on where the event comes from.
- Switching a trigger off stops new events, lets the current run finish, and keeps waiting events for when it is switched on again. The user can clear them.

Trigger definitions are plain files beside the workflows. Whether a trigger is on is remembered per machine and starts as off in a copied project. Enabled triggers start when the backend's catalog is ready.

## Using it

One backend runs per project folder. Browser and command line share capabilities and checks; page, help, stop and plugin status answer during startup, while other work waits for the catalog.

- The frontend is the main way to build. It is generated from node descriptions, so plugins ship no frontend code. An input becomes a field (including file paths), a secret picker, a socket, or a box, by its type.
- The command line runs nodes, shows runs, and manages triggers and plugins. Workflows are short text files that can be read and edited by hand; that is the advanced route.
- A command that needs the project connects to its backend and starts it if none is running. The backend keeps running and says when switched-on triggers start. `inode stop` stops it.

## Plugins

A plugin is a folder with a `plugin` manifest (`name`, `version`, `doc`, `accesses`, optional `setup`) and `.node` files. Optional `process` uses the same string/list argument rule as `setup`, without a shell; `env: {API_KEY: stored_key}` binds environment variables to secret names, shown before installation. Its working folder is the installed plugin folder; its environment contains PATH/HOME/TMPDIR/LANG plus these bindings.

Processes start on installation and together on backend startup; each shows `starting` until it describes its nodes or fails. They run until removal/replacement/shutdown: close stdin, wait briefly, then end the process group. Nodes join `.node` files as `plugin.node`. Requests and replies use stdin/stdout, one JSON object per line (replies up to 1 MiB), with string ids; stderr is bounded diagnostics. Only three messages:

- **describe:** `{"id":"1","method":"describe"}` → `{"id":"1","nodes":{"next":{"doc":"Advance counter.","inputs":{},"outputs":{"count":"number"}}}}`. Node descriptions use the usual ports and optional `no_outside_effect`, without `run`.
- **run:** `{"id":"2","method":"run","node":"next","inputs":{},"dir":"/project/.inode/work/plugin-xyz"}` → `{"id":"2","outputs":{"count":1}}`, or `{"id":"2","error":"what went wrong"}`. Files must be closed before replying: a declared file leaf may be an absolute path inside `dir`; the host imports it before checking/publishing outputs, then removes `dir`. Existing stored references also work.
- **cancel:** `{"id":"2","method":"cancel"}` asks the producer to stop that run. Reply once to the original run after work stops, for example `{"id":"2","error":"cancelled"}`; no separate cancel reply. The host retains `dir` until that reply or process exit and discards late outputs. Cancellation cannot undo outside actions.

Keep reading while runs execute: replies may arrive out of order and ids identify their runs. Exit/unreadable stdout fails pending runs, warns they may or may not have finished, marks the process unavailable and makes checks report the reason on `use`; add the plugin again to recover. There is no automatic replay. Plugin-to-core requests, child calls, state, live objects and plugin triggers are not built yet.

- **Discover:** an index that is only a list of names and URLs; no hosted service is required.
- **Install:** always an explicit action, from a name, a git URL or a local path, after showing what the plugin says it accesses. A plugin is trusted code; nothing is enforced. Adding a plugin again replaces it. Each plugin gets its own process and environment, so plugins cannot break each other's dependencies.
- **Use:** nodes appear as `plugin.node`. A project lists the plugins and versions it uses. Opening a project reports what is missing; it never installs by itself.

**Adapters**, in order of value:

1. **Command-line tools.** Ordinary `.node` descriptions bind arguments, flags, stdin, stdout and environment without a shell. File bindings follow with the managed file store; wrapping ffmpeg or pandoc then needs only a description.
2. **MCP servers.** Paste a server command into Plugins, edit the name and secret bindings, then confirm the ordinary plugin that runs `inode mcp -- <server command>`. Each tool becomes a node with conservative schema-derived ports and `result: {response, files}` (text/structured data and stored media); Add again rediscovers tools.
3. **OpenAPI.** Paste an OpenAPI 3 document's file or URL into Plugins, edit the name, base URL and secret bindings, then confirm the ordinary plugin that runs `inode openapi -- <document> <base URL>`. Each operation becomes a node with schema-derived ports and `result: {status, body, files}`; these are API calls, not finished integrations, so paging, polling and sign-in are built on top as workflows.
4. **Library functions.** Signature to ports, as scaffolding for functions that already fit.
5. **ComfyUI.** One node submits a whole graph to a running ComfyUI server and returns its output files. Hosting individual ComfyUI nodes is not planned.
6. **n8n nodes.** Not adapted; they depend on n8n's item runtime and credentials. The same services are reached through OpenAPI or MCP.

**Core and plugins:**

| Core (the backend) | Plugins, including a bundled standard one |
|---|---|
| Load and check workflows | Every capability: http, files, text, AI models, media |
| Runs, child runs, records | `map`, `choose`, `repeat`, `retry`, `try`, `agent` |
| Triggers: definitions, on/off, waiting events | Trigger kinds: schedule, web request, folder |
| File store, secret store, plugin state | Program descriptions; MCP, OpenAPI, ComfyUI adapters |
| Plugin host and the interface used by both clients | |

The core contains no special nodes. The bundled plugin follows the same contract as any other, and the core does not recognise `map` or `choose` by name.

## Example: daily reading digest

```yaml
# summarize_article.flow
inputs:  {url: text}
steps:
  page:    {use: http.get,     in: {url: =inputs.url}}
  article: {use: html.to_text, in: {html: =page.body}}
  summary: {use: llm.ask,      in: {prompt: "Summarize in two sentences.", text: =article.text}}
outputs: {summary: =summary.text}
```

```yaml
# digest.flow
inputs:  {feed: text}
steps:
  items:     {use: rss.read,   in: {url: =inputs.feed, limit: 5}}
  summaries: {use: map,        in: {over: =items.links, do: summarize_article}}
  message:   {use: text.join,  in: {parts: =summaries.result, separator: "\n\n"}}
  sent:      {use: slack.post, in: {channel: "#reading", text: =message.text, token: slack_token}}
outputs: {ok: =sent.ok}
```

`summarize_article` is `node(text) gives text`, so `map` over a list of links gives a list of summaries. Every step in it only reads, so it could also sit in a `retry` box; `digest` could not, because `slack.post` has an outside effect. `token` is a `secret` input, so `slack_token` is the name of a stored secret. To send it every morning: `inode trigger add schedule.daily at=08:00 run=digest feed=https://…`.

## The one-page tutorial

[The tutorial](tutorial.md) is the one-page acceptance test, shared by the Guide, `inode help` and agents.

## Writing a node

A `.node` file describes a program as a node. In `nodes/<name>.node`, declare `doc`, inputs, outputs, optional `no_outside_effect` and `run`. Programs run without a shell. `run` accepts quoted words with `~/` expansion, an argument list, or `{args: [...]}`; shell operations are refused and literal shell characters require quoting or a list.

| Binding | What it does |
|---|---|
| `args: [..., =inputs.x]` | One argument per scalar/list item. |
| `{flag: --name, value: =inputs.x}` | True sends flag; false omits; scalar/list repeats flag/value pairs. |
| `stdin: =inputs.x` | File bytes or text unchanged; otherwise JSON. |
| `stdout: count` | One output: file bytes (labelled `count.bin`), text unchanged, otherwise one JSON value. |
| `env: {TOKEN: =inputs.token}` | Scalars; secrets only here or stdin. |
| `files: {in.md: =inputs.source, out.html: =outputs.html}` | Stage/collect files under relative names used in args; one producer per output. |

Without bindings, streams carry JSON objects of all inputs/outputs. File bindings leave JSON stdout for remaining outputs, or unused when none remain. `files` selects a fresh private folder, removed afterwards; otherwise programs run in the description folder. Bad bindings fail at load; nonzero exit fails with bounded scrubbed stderr. Plugin `setup` uses the same string/list rule. `file.save(file, path)` copies a result to disk; text `file.read`/`file.write` stay unchanged. File inputs and file.save paths accept `~/` or start at the project folder. See [file forms](tutorial.md#the-two-file-forms).

## Limits

These are deliberate. Each keeps the model small.

| Not provided | What to do instead |
|---|---|
| Automatic recovery or reuse of past results | Run again is always fresh. Run from here is the explicit exception. |
| Exactly-once outside actions | Interrupted runs are marked and left to the user. |
| `retry` around a step that writes | A plugin retries its own writes internally, where it knows its guarantees. |
| `retry` around a node that a workflow received as its own input | Its effect is unknown, so it is refused. Put the `retry` where the node is chosen. |
| Streaming data between nodes | A node returns once. Split the data and `map` over it. Progress messages are display only. |
| Waits and approvals that survive a restart | A waiting run holds its place only while the backend stays up. |
| Cleanup guaranteed after failure | `try(do, on_error)` covers the common case; there is no "finally". |
| Using one plugin's live objects in another plugin | Exchange files. |
| Transforming secrets inside a workflow | A node that needs a secret takes a `secret` input and uses it internally. |
| Noticing changed node code in Run from here | Records keep what each step was given and returned, not the program behind it. The preview shows where each reused value came from and how old it is. |
| Seeing every file in a watched folder | The folder kind looks about once a second. Files already there when it is switched on are not events, a file removed before it is seen is missed, and a file can be reported before another program has finished writing it. |
| Coordinating state shared between workflows | Only runs of one trigger are kept from overlapping. Plugins that keep state guard it themselves. |
| Formulas inside references | References select data by path only; computation is a node. |
| Enforced limits on what a plugin can do | Plugins are trusted code; install only what you trust. |
| Full MCP conformance | Stdio, 2026-07-28, and earlier revisions for tools only; schemas are projected to iNode types without full validation, which MCP requires clients to perform. No HTTP, input rounds, sampling, roots, resource operations, prompts/completion, tasks, Apps or push catalog updates. File arguments use the server's JSON contract; inline wire frames are buffered and limited to 8 MiB. |
| Full OpenAPI coverage | OpenAPI 3 documents up to 8 MiB with local `$ref`s; an operation with any optional parameter takes one `arguments` object, and inputs are not validated against the schema. JSON request bodies only; JSON and text responses up to 8 MiB are data, other content becomes files, a status of 400 or above fails the run, and response headers are not returned. Only GET and HEAD are marked as having no outside effect. Credentials are http bearer/basic and header/query API keys, each read from the variable named after its security scheme; no OAuth flows, cookies or mutual TLS. |
| Managing iNode from another machine | The backend serves the local machine only. |
| Automatic removal of stored files | Archive/move whole projects including `.inode/`, or remove unneeded projects. Deleting blobs breaks run records. |
| Importing n8n or ComfyUI workflow files | Adapters reuse their capabilities, not their formats. |
