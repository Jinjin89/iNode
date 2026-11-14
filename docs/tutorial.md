# iNode in one page

Everything you use or build in iNode is a **node**: it takes named inputs, does one job and gives back named outputs.
Connect nodes into a **workflow**. Save that workflow and it becomes a node too, so small pieces become bigger pieces.

## Three words
- A **node** is like a recipe. It says what goes in and what comes out.
- A **run** is cooking the recipe once. iNode keeps what went in and came out of every step, so you can look afterwards.
- A **trigger** is like an alarm clock. It says, “When this happens, run that.”

A **step** is one use of a node in a workflow.

A **box** is a slot on a step that holds another node for that step to call; a node in a box never runs by itself.

A **plugin** is a folder of more nodes.
```text
┌─ upper ───────┐  step name
│ text.upper    │  node used
● text [ hi ]   │  input: type or
│               │  connect to dot
│        text ● │  output: drag
└───────────────┘  to an input
```

## Try it
Click a step to edit; Escape closes it. Shift+click steps, **Make a node**, open it, then **Save as** to reuse it.

**1. Start a project.**
- Copy `examples/` to your own folder.
- Go into that folder.
- Run `inode start`.
- Open the whole printed address, including the part after `#`.

Canvas opens an empty new workflow.

**2. Run one node.**
- Find `text.upper` in the list on the left.
- Click it to add a step.
- Type `hi` into its `text` field.
- Press **Run**.

The step returns `HI`. You are now looking at a **run**, a read-only record.
- Press **Back to editing** to return to the workflow.

**3. Connect steps.**
- Add `text.join`.
- In `parts`, click **Add item** twice.
- Type `Hello` in the first row.
- Drag a line from the first step's `text` output to the second row's dot.
- Add a second `text.upper`.
- Connect `text.join`'s output to the last step's `text` input.
- Press **Run**.
- Click a step to see **Received** and **Returned**.

The last step returns `HELLO HI`.

**4. Make your own node.**
- Press **Back to editing**.
- Click the first step's `text` row.
- Point at its field and choose **Ask for this when run**.
- Click the last step's `text` output row and choose **Give this back**.
- Press **Save**.
- Name the workflow `welcome`.
- Press **Save** again.

Your saved workflow is now a node with one input and one output, listed under **Your workflows**.

**5. Do it many times.**
`map` needs to know what to do for each item, so you give it a node in its box.
- Press **New**.
- Add `map`.
- Find `welcome` in the list.
- Drop it into the `do` box.
- In `over`, click **Add item**.
- Type `hi`.
- Add a second item.
- Type `yo`.
- Choose **Give this back** on `result`.
- **Save** as `many_welcomes`.
- Press **Run**.

The result has two greetings, one per word.
- Click `map`.
- Click **Call 1** to look inside one call.

**6. Pass on part of a result.**
You can give back just one item instead of the whole list.
- Click `many_welcomes` in the run bar to return to the whole run.
- Click `map`.
- Open `result` under **Returned**.
- Point at its first item.
- Choose **Use this**.
- Back in the editor, click the dot beside `result` on the Outputs card.
- Press **Run**.

The workflow now gives back only the first greeting.

**7. Run without you.**
- Open **When**.
- Choose `schedule.every`.
- Set `interval` to `5` (seconds).
- Choose `welcome`.
- Set `text` to `hi`.
- Click **Add trigger**.
- Switch the new row on.
- Open **Runs** to see the runs it starts.
- Return to **When**.
- Switch the trigger off again.

**8. Get more nodes.**
- Open **Plugins**.
- Enter `tiny_plugin`.
- Click **Add plugin**.
- Read what the plugin says it accesses.
- Click **Install plugin** to confirm.

Its nodes appear in the node list. An MCP server is a plugin; paste its command here to use its tools as ordinary nodes. Any program that reads and prints JSON (named values written as text) can be a node. Enter a path for a file input; use `file.save` to copy a returned file where you want it.

## The same from the command line
```sh
# List the available nodes.
inode nodes
# See a node's inputs and outputs.
inode show text.upper
# Check the workflow saved above.
inode check welcome
# Run it once.
inode run welcome text=cat
# Call it once per word.
inode run map 'over=[hi, yo]' do=welcome
# Add a trigger, initially off.
inode trigger add schedule.every interval=5 run=welcome text=hi
# List triggers and their states.
inode trigger list
# List past runs and their ids.
inode runs
# Use an id from the list above.
inode runs show <id>
# Stop the background backend.
inode stop
```

## The two file forms
A `.flow` filename names its workflow node. References select outputs (`=upper.text`) or inputs (`=inputs.text`), inside lists and objects too. Dots select fields or items (`.0` is first); `==` writes a literal `=`.
```yaml
# shout.flow
inputs: {text: text}
steps:
  upper: {use: text.upper, in: {text: =inputs.text}}
outputs: {text: =upper.text}
```

A `.node` file turns a program into a node. Here `wc -w` counts words in `text`, returning `count`. Without `stdin`/`stdout`, programs read all inputs as one JSON object and print all outputs as one.
```yaml
# nodes/word_count.node
doc: Count words.
inputs: {text: text}
outputs: {count: number}
run: {args: [wc, -w], stdin: =inputs.text, stdout: count}
```

## When something goes wrong
The checker reports structural mistakes before anything runs, naming the step, input and what to change. A node can still fail while running; the run selects the failed step and shows its message.
