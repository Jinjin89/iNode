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

Types are names, checked when connecting, with no implicit conversion:

| Type | Meaning |
|---|---|
| `text`, `number`, `bool`, `any` | Plain data. |
| `list of T`, `object{name: T, …}` | A list with its item type; an object with named fields. Plain `list` and `object` leave the contents unstated. |
| `file` | A reference to a file kept by the core, identified by its contents. This is how plugins exchange large or binary data. |
| `secret` | The name of a stored secret. |
| `node(T) gives U`, `node() gives U` | A node to be called by the receiver: the type of its one open input, if it has one, and of its single output. |
| plugin-named handles | A live object such as a loaded model. Only nodes of the plugin that created it can operate on it, and it lives as long as that plugin's process. |

- A capital letter in a description (`T`, `U`) stands for "the same type wherever this letter appears" in that one use of the node.
- Where a type is `any` or an unstated `object`, the connection is allowed and shown as unchecked. The value is checked against what the consumer expects before the consumer starts, and a node's outputs are checked against its description when it returns.
- Secret names and handles can be passed through workflows, boxes and control nodes as opaque references. A secret is turned into its real value only when the core calls a node that is not a workflow and declares a `secret` input; a control node that merely passes one along sees only the name. A secret named in a workflow but not stored is reported as a problem before anything runs. Records show secret inputs by name. If the node's outputs or error contain the value exactly, it is replaced by the name before anything is recorded or passed on; an encoded or transformed copy is not recognised, and a plugin is trusted code that can still send the value elsewhere.

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

Trigger definitions are plain files beside the workflows. Whether a trigger is on is remembered per machine and starts as off in a copied project. Triggers that are on are active whenever the backend is running.

## Using it

One backend runs per project folder. The browser frontend and the command line are two clients of it with the same capabilities and the same checks.

- The frontend is the main way to build. It is generated from node descriptions, so plugins ship no frontend code. An input becomes a field, a picker (files, secrets), a socket, or a box, by its type.
- The command line runs nodes, shows runs, and manages triggers and plugins. Workflows are short text files that can be read and edited by hand; that is the advanced route.
- A command that needs the project connects to its backend and starts it if none is running. A backend started this way keeps running and says so, including that switched-on triggers are now active. `inode stop` stops it.

## Plugins

A plugin is a folder with a manifest (name, version, a description, what it says it accesses, and an optional setup command that prepares its own environment) and nodes. The contract:

- **describe** returns the descriptions of its nodes and trigger kinds.
- **run(node, inputs)** returns outputs or an error, with optional progress messages for display.
- **Requests to the core** while running: call a node it was given, read or write a file, read or store its own secrets and small state.
- **For a trigger kind:** activate a trigger with its settings, submit events, deactivate it. Events carry only plain data and files.

The simplest node is a description file plus any program that reads its inputs as JSON and prints its outputs as JSON (see "Writing a node"). Plugins that keep models loaded or call other nodes run as a long-lived process and must accept new runs while waiting on a call.

- **Discover:** an index that is only a list of names and URLs; no hosted service is required.
- **Install:** always an explicit action, from a name, a git URL or a local path, after showing what the plugin says it accesses. A plugin is trusted code; nothing is enforced. Adding a plugin again replaces it. Each plugin gets its own process and environment, so plugins cannot break each other's dependencies.
- **Use:** nodes appear as `plugin.node`. A project lists the plugins and versions it uses. Opening a project reports what is missing; it never installs by itself.

**Adapters**, in order of value:

1. **Command-line tools.** A bundled generic command node turns inputs into arguments and files and collects output files, so wrapping ffmpeg or pandoc needs only a description.
2. **MCP servers.** Each tool becomes a node. Tools without an output schema return one `result` object. Tools that ask for more input mid-call are not supported.
3. **OpenAPI.** Each operation becomes a node. This gives API calls, not finished integrations; paging, polling and sign-in are built on top as workflows.
4. **Library functions.** Signature to ports, as scaffolding for functions that already fit.
5. **ComfyUI.** One node submits a whole graph to a running ComfyUI server and returns its output files. Hosting individual ComfyUI nodes is not planned.
6. **n8n nodes.** Not adapted; they depend on n8n's item runtime and credentials. The same services are reached through OpenAPI or MCP.

**Core and plugins:**

| Core (the backend) | Plugins, including a bundled standard one |
|---|---|
| Load and check workflows | Every capability: http, files, text, AI models, media |
| Runs, child runs, records | `map`, `choose`, `repeat`, `retry`, `try`, `agent` |
| Triggers: definitions, on/off, waiting events | Trigger kinds: schedule, web request, folder |
| File store, secret store, plugin state | Adapters: command, MCP, OpenAPI, ComfyUI |
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

> # iNode in one page
>
> **The one idea.** A node takes named inputs, does one job, and returns named outputs. Everything you use or build in iNode is a node.
>
> **1. Start.** `inode start` opens the editor in your browser.
>
> **2. Run a node.** Find `http.get` in the node list and drag it onto the canvas. Type a URL into its `url` field and press **Run**. The result appears on the node.
>
> **3. Connect nodes.** Drag in `html.to_text` and draw a line from `body` to `html`. Press **Run**, then click either step to see what it received and what it returned.
>
> **4. Make it your own node.** Click the `url` field and choose **Ask for this when run**. Click the `text` output and choose **Give this back**. Save as `page_text`. It is now in your node list, with one input and one output, like any other node.
>
> **5. Do it many times.** Start a new workflow. Drag in `map`, drop `page_text` into its `do` box, and type a few URLs into `over`. Press **Run**. A node inside a box does not run by itself; `map` runs it once for each item.
>
> **6. Run it without you.** Open **When**, choose "every day", set a time, choose what to run, and switch it on.
>
> **7. Get more nodes.** Open **Plugins** and install one. Its nodes appear in your node list.
>
> **From the command line.** The same things work there: `inode run page_text url=https://example.com`, `inode runs`, `inode trigger add …`, `inode add <plugin>`. Workflows are saved as short text files you can read; editing them by hand is the advanced route.

## Writing a node

For plugin authors. Create `nodes/count_words.node`:

```yaml
doc: Count the words in a text
inputs:  {text: text}
outputs: {count: number}
no_outside_effect: true
run: python count_words.py
```

and `nodes/count_words.py`:

```python
import json, sys
text = json.load(sys.stdin)["text"]
json.dump({"count": len(text.split())}, sys.stdout)
```

Any program that reads its inputs as JSON and prints its outputs as JSON can be a node. `count_words` is now in the node list. A plugin is a folder of such nodes with a manifest.

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
| Managing iNode from another machine | The backend serves the local machine only. |
| Importing n8n or ComfyUI workflow files | Adapters reuse their capabilities, not their formats. |
