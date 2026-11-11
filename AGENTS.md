# iNode

iNode is a local, self-hosted workflow framework inspired by n8n and ComfyUI, built on three concepts: node, run and trigger. It has one backend and two clients of it, a browser frontend and a command line, which can do the same things. The project is at the conceptual design stage. `docs/design.md` is the current design and the source of truth.

The aim is that anybody can use iNode with almost nothing to learn first: no usage rules to memorise, mistakes prevented by the tool, and a model small enough to explain on one page. Every rule below protects that.

## Concepts before code

- Solve a new case with an existing concept first. Most needs should become a node or a plugin, not a new core mechanism.
- When the existing concepts really cannot cover a case, look for one general idea that covers it and its neighbours, rather than a specific feature or special case. One concept that removes three methods is better than three methods.
- Any new core concept has to fit in the one-page tutorial in `docs/design.md`. If it needs its own paragraph there, treat that as a cost and say so when proposing it.
- Keep the core small. Capabilities belong in plugins, and the core should contain no special nodes.
- If a feature only works when the user remembers a rule, redesign it so the frontend and command line make the wrong thing impossible or the right thing obvious.
- The frontend and command line stay equal. Add a capability to the backend interface once and expose it in both; plugins ship no frontend code.

## Code

- Keep code concise and consistent. Before writing something, check how the same thing is already done in the repo and do it that way: same naming, same structure, same error handling.
- Prefer removing or generalising code over adding more. If a change makes an older helper, option or code path unnecessary, delete it in the same change.
- Do not add options, abstractions or layers for cases that do not exist yet.

## Docs

- The docs should stay about the same size as the project grows. Update the existing text in place so it describes how things are now; do not append new sections, changelogs or notes about what changed.
- One topic lives in one place. If something is already described, edit it there instead of describing it again elsewhere.
- When a decision changes, rewrite the affected part and remove what no longer holds. Git history is the record of what it used to say.
- Create a new doc file only when a topic has no existing home. Reviews and other working notes go in `docs/` and are deleted once their conclusions are folded into the design.
