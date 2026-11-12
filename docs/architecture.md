# iNode architecture

How the model in `design.md` is built. This document follows the model and never the other way round: it may only use concepts defined there (node, run, trigger, plugin). If something here would require the user to learn a new idea, either `design.md` is changed on purpose or this document is simplified.

**Built so far:** the core (catalog, checker, runner, run records), boxes with the control nodes `map`, `choose`, `repeat`, `retry` and `try`, a few built-in nodes, nodes that are programs, plugins installed from a folder or a git repository, secrets, the long-running backend with its interface, the command line as a client of it, triggers with the built-in kinds `schedule.every`, `schedule.daily` and `folder.new_file`, run from here, and the frontend's Canvas, Runs, When, Plugins and Secrets screens. Not built yet: "move into box", the web-request trigger kind, trigger kinds from plugins, long-lived plugin processes, `agent`, plugin search.

## Shape

One backend per project folder. Two clients, the browser frontend and the command line, use one interface to it. Neither client contains rules of its own, so they cannot drift apart.

The interface is grouped by what the user manages:

| Group | Operations |
|---|---|
| Help | tutorial text |
| Nodes | list, describe |
| Workflows | open, save (a save based on an out-of-date copy is rejected), check, "what can connect here" |
| Runs | start, stop, list, show, watch live, run again, run from here |
| Triggers | add, remove, list, switch on or off, clear waiting events |
| Plugins | search, list missing, install, remove |
| Secrets | set, list names, remove |

## Backend

Six parts, each with one job.

| Part | Job |
|---|---|
| **Catalog** | Knows every node and trigger kind: those described by plugins, and the workflows and nodes in the project folder. |
| **Checker** | Decides whether a workflow is well formed. Returns a list of problems, each naming the step, the input and what to change. |
| **Runner** | Performs runs. Starts each step when what it references or comes after is ready, starts child runs, applies the failure rule, and reports every change to the store and to watchers. |
| **Plugin host** | Installs plugins, starts and stops their processes, passes describe and run to them, and passes their requests (call a node, files, secrets, state) back. |
| **Trigger service** | Loads trigger definitions, checks each as the two-step workflow it is, starts and stops watching for its kind, stores each accepted event as a waiting run, and starts those runs one at a time, in order. |
| **Store** | Keeps run records (including those still waiting for their trigger), files, secrets, plugin state and which triggers are on. |

Rules that hold the parts together:

- **One checker.** It is the single implementation of workflow-structure checking, type compatibility, declared input constraints, cycles through saved workflows and boxes, and the "no outside effect" inference. The frontend asks it instead of deciding itself: after each edit it sends the workflow as it stands, saved or not, and draws the answer, which lists the problems, the references found (the lines on the canvas) and each step's inputs and outputs with their types filled in. Plugins remain responsible for validation specific to their operations at run time.
- **One entry to the runner: start a run of a node.** A press of Run, a command, a trigger event, a plugin's call and "try this node alone" all arrive the same way. Run from here is the same entry, given an inline workflow made of the selected steps with the copied values as its inputs; the new run's record links to the run and step it came from.
- **The runner executes a checked snapshot.** It runs the definitions as they were when checked, and does not look up definitions that may change during the run. The snapshot is kept with the record.
- **A node is performed by one of two things.** The runner itself performs workflows; a plugin performs everything else. The bundled standard nodes form a plugin that lives inside the backend process and obeys the same descriptions, checks and child-call contract as any other. The runner does not recognise `map` or `choose` by name.
- **Waiting does not block children.** A run waiting on child runs does not occupy a scheduler execution slot needed by those children.
- **Values are checked at the edges.** A value arriving through an unchecked connection is checked against what the consumer expects before the consumer starts, and a node's outputs are checked against its description when it returns.
- **Records form a tree.** Each run records its node, inputs, parent run, and for every step its state (waiting, running, done, failed, interrupted), what it received and what it returned. Data is stored as is, files by reference to unchanged contents, secrets by name, live objects as a placeholder.
- **An accepted event is a run record.** Accepting an event means writing a run record in the waiting state, forced to disk before the source is told it was accepted. There is no separate queue to hand over from: a trigger's queue is its records that are waiting and have never started. Leaving the queue is the same record being marked started, also forced to disk, so after a crash a record is either still queued or started and therefore interrupted, never both and never neither. Ordinary progress of a run is written without that cost.

### One run, end to end

`inode run digest feed=https://…`

1. The command line connects to the project's backend, starting it if needed, and asks to start a run of `digest`.
2. The checker validates `digest` and its inputs. Problems are returned and nothing starts.
3. The runner snapshots the definitions, creates the run record and starts every step that is ready.
4. For a plugin's node, the plugin host sends run to that plugin. For a workflow, the runner starts a child run. When `map` asks to call `summarize_article`, the request comes back through the plugin host and becomes a child run.
5. Each change is written to the store and sent to anyone watching.
6. When the last step finishes, the outputs are returned and printed.

The frontend's Run button does exactly the same, and draws the live updates on the canvas.

## Frontend

The frontend draws what the backend says and sends back what the user did.

The Guide panel stays beside the work and opens when a project has no runs. Its text comes from the read-only help operation: `docs/tutorial.md`, embedded in the binary and also printed by `inode help` without starting a backend.

Five screens:

- **Canvas.** Node list on the side; steps with their inputs and outputs; lines for references and a different line for `after`. A `node` input is a box that holds a saved node or steps built inside it; a box with steps opens into the same canvas, full size, with a way back. A value entering a box from outside is drawn crossing the box edge once. "Move into box" is offered for a step when the checker confirms that everything using it (references, captured references, `after`, workflow outputs) stays valid. Unchecked connections are marked. Clicking a step shows its fields, and after a run what it received and returned. A step that called other nodes opens into its child runs.
- **Runs.** Every run, newest first, with what started it. Opening one shows the canvas as it was, with results. Run again and Run from here are offered here. Run again starts a fresh run with the same inputs: of the node as it is now, or, when the run was of a workflow with unsaved changes, of that run's own snapshot. Run from here shows the selected part and the copied values, each with its source, before starting.
- **When.** Triggers, each with an on/off switch, its current run, the number of waiting events, its last run and its last failure.
- **Plugins.** Search, install, remove; what a project is missing, with what each plugin says it accesses.
- **Secrets.** Add, replace and remove by name. Values are never shown again.

A field is chosen by the input's type: text box, number box, switch, list editor, file picker, secret picker, socket only (for live objects, which cannot be typed in), or box. Each described port carries its type as display text and as structure from the same type tree, so clients do not parse the notation.

## Command line

A thin client of the same interface.

| Command | Does |
|---|---|
| `inode run <node> key=value` | starts a run and prints its outputs |
| `inode nodes`, `inode show <node>` | lists and describes nodes |
| `inode runs`, `inode runs show/follow/stop/again <id>` | lists, shows, follows, stops and repeats runs |
| `inode runs from <id> <step> [--start]` | previews a run from that step of a past run; `--start` starts it |
| `inode trigger add <kind> key=value run=<node> key=value` | adds a trigger, which starts off and is given a name such as `schedule.daily-1` |
| `inode trigger list/on/off/clear/remove` | lists triggers and what each does, switches one on or off, clears its waiting events, removes it |
| `inode secret set <name>`, `inode secret list/remove` | stores a secret, asking for the value without showing it or reading it from standard input, never from the command line; lists names; removes one |
| `inode search`, `inode add <plugin>` | finds and installs plugins |
| `inode start`, `inode stop` | starts the backend and opens the frontend; stops the backend |

- Commands that need the project connect to its backend and start it if none is running. Help, version and shell completion do not start it, and `inode stop` never starts one in order to stop it.
- A backend started by a command keeps running. The command says that it started it, that switched-on triggers are now active, and how to stop it.
- Problems from the checker are printed in the same words the frontend shows.

## Files on disk

```
my-project/
  digest.flow           a workflow (shared)
  nodes/                your own nodes: description + program (shared)
  triggers/             one file per trigger: two steps, event and run (shared)
  plugins               the plugins this project uses, with versions (shared)
  .inode/               the backend's private data (not shared):
                        installed plugins, run records, files, secrets,
                        which triggers are on
```

- Everything outside `.inode/` is plain text that can be read, edited by hand, and put under version control. Saving a workflow from the frontend rewrites its file, so comments written by hand in it are lost.
- Having a project's files is not permission to run its contents. A copied project reports its missing plugins and waits for an explicit install; its triggers start as off; its secrets are asked for.
- A `.flow` file may carry a `view` part with the positions of steps on the canvas. It is preserved when saving and in the snapshots kept with run records, and has no effect on checking, running, or deciding whether a workflow has changed.

## Reach

The control interface listens only on the local machine and requires a credential that is created each time the backend starts and given to the two clients automatically through a private file in `.inode/`. Browser requests and live connections are accepted only from the frontend's own address, so another web page cannot drive the backend. Web requests received for triggers arrive on a separate address and never reach the control interface. Managing iNode from another machine is not provided.

## Stack

| Part | Choice | Reason |
|---|---|---|
| Backend | Go | Ships as one executable with the built frontend inside, which makes installation one step. Cheap concurrent runs and process supervision are what the runner and plugin host need. |
| Frontend | TypeScript, React, React Flow | React Flow is the usual library for node canvases and can draw nodes inside nodes. That is only drawing; the backend stays the authority on what a box means. The built page is kept in the repository (`web/dist`), so building iNode needs only Go; `./build.sh` rebuilds both, and `./build.sh --check` fails when the built page does not match its source. |
| Storage | Plain folders: one JSON file per run. SQLite when listing and searching runs need it. | Local, no server, simple to back up. |
| Client to backend | JSON over HTTP: a request names one operation and its arguments, and the reply is one JSON line per value, or one error. Following a run is a reply that stays open and sends the run's current record at each change; a slow follower skips to the newest. | Any language can call it, and it needs nothing beyond HTTP. |
| Plugins | Separate processes exchanging JSON; a small Python helper library for authors | Plugins are not tied to the backend's language. |
| Python plugins | Own environment per plugin, created at install | Installation also provides a suitable Python or reports clearly that one is missing. |

The bundled standard nodes are written in Go, so a new user needs no Python until they install a Python plugin.

A desktop app is a possible later addition: a window around the same frontend, made with a wrapper such as Wails or MyGo. The backend does not depend on one, because it must also run with no window, and the wrapper's own way of calling Go from the page is not used, because the command line could not share it.
