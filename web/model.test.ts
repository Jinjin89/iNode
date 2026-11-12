import { describe, expect, it } from 'vitest';
import { add, box, connect, connectionProblem, content, emptyReport, emptyValue, expose, graph, groups, handle, history, kind, move, parseHandle, layout, reference, rename, runWorkflow, set, type, workflowDescription, type Workflow, type Description, type Reference, type History } from './model';

const catalog: Description[] = [
  { name: 'text.upper', inputs: { text: 'text' }, outputs: { text: 'text' } },
  { name: 'map', inputs: { over: 'list of T', do: 'node(T) gives U' }, outputs: { result: 'list of U' } },
];
const tiny: Workflow = {
  inputs: { text: 'text' },
  steps: { a: { use: 'text.upper', in: { text: '=inputs.text' } }, b: { use: 'text.upper', in: { text: '=a.text' }, after: ['a'] } },
  outputs: { text: '=b.text' },
};
const refs: Reference[] = [
  { step: '(inline)/a', input: 'text', from: ['inputs', 'text'], path: ['steps', 'a', 'in', 'text'] },
  { step: '(inline)/b', input: 'text', from: ['a', 'text'], path: ['steps', 'b', 'in', 'text'] },
  { step: '(inline)', input: 'outputs.text', from: ['b', 'text'], path: ['outputs', 'text'] },
];
const report = { ...emptyReport, references: refs };

describe('document edits', () => {
  it('ignores only root view for checking and choosing a saved definition', () => {
    const moved = set(tiny, ['view', 'positions', handle(['steps', 'a'])], { x: 2, y: 3 });
    expect(content(moved)).toBe(content(tiny));
    expect(JSON.stringify(moved)).not.toBe(JSON.stringify(tiny));
    const inline = set(tiny, ['steps', 'm'], { use: 'map', in: { do: { use: tiny } } });
    expect(content(set(inline, ['steps', 'm', 'in', 'do', 'use', 'view'], { x: 2 }))).not.toBe(content(inline));
    const literal = set(tiny, ['steps', 'a', 'in', 'data'], { view: 1 });
    expect(content(set(literal, ['steps', 'a', 'in', 'data', 'view'], 2))).not.toBe(content(literal));
  });
  it('adds and deletes a step without repairing dangling references', () => {
    const added = add(tiny, 'text.upper');
    expect(added.steps?.upper).toEqual({ use: 'text.upper' });
    expect(tiny.steps?.upper).toBeUndefined();
    expect(add(added, 'text.upper').steps?.upper2).toEqual({ use: 'text.upper' });
    expect(add(tiny, 'plugin.text.join').steps?.join).toEqual({ use: 'plugin.text.join' });
    const deleted = set(tiny, ['steps', 'a'], undefined);
    expect(deleted.steps.a).toBeUndefined();
    expect(deleted.steps.b.in.text).toBe('=a.text');
    expect(tiny.steps?.a).toBeDefined();
  });
  it('uses one path edit for values, inputs, outputs, boxes and positions', () => {
    for (const [path, value] of [
      [['steps', 'a', 'in', 'text'], 'hi'], [['inputs', 'n'], 'number'], [['outputs', 'n'], 2],
      [['steps', 'm', 'in', 'do', 'in', 'text'], 'ok'], [['view', 'positions', handle(['steps', 'a'])], { x: 2, y: 3 }],
    ] as [any[], any][]) {
      const changed = set(tiny, path, value);
      expect(path.reduce((part, key) => part[key], changed)).toEqual(value);
      expect(path.reduce((part, key) => part?.[key], set(changed, path, undefined))).toBeUndefined();
    }
    expect(set(['a', 'b'], ['0'], undefined)).toEqual(['b']);
    const named = set(tiny, ['steps', '__proto__'], { use: 'text.upper' });
    expect(Object.keys(named.steps)).toContain('__proto__');
    expect(Object.getPrototypeOf(named.steps)).toBe(Object.prototype);
    expect(set({ value: 1, other: 'kept' }, ['value'], (value: number) => value + 1)).toEqual({ value: 2, other: 'kept' });
  });
  it('draws a connection without deciding whether its types fit', () => {
    expect(connect(tiny, ['a', 'text'], ['steps', 'b', 'in', 'number']).steps?.b.in?.number).toBe('=a.text');
    expect(connect(tiny, ['b'], ['steps', 'a', 'after']).steps?.a.after).toEqual(['b']);
    expect(connect(tiny, ['b', 'text'], ['steps', 'a', 'in', 'after']).steps?.a.in?.after).toBe('=b.text');
    expect(connect(tiny, ['b', 'text'], ['outputs', 'after']).outputs?.after).toBe('=b.text');
  });
  it('renames exactly the reported locations, plus after and the saved position', () => {
    const flow = set(tiny, ['steps', 'm'], { use: 'map', in: { over: ['=a.text', { 'a.b': '=a.text' }], do: { use: { outputs: { text: '=a.text' } }, in: { x: '=a.text' } } } });
    const extra = [
      { ...refs[1], path: ['steps', 'm', 'in', 'over', '0'] },
      { ...refs[1], path: ['steps', 'm', 'in', 'over', '1', 'a.b'] },
      { ...refs[1], path: ['steps', 'm', 'in', 'do', 'in', 'x'] },
    ];
    const renamed = rename(flow, 'steps', 'a', 'upper', { ...report, references: [...refs, ...extra] });
    expect(renamed.steps!.b.in!.text).toBe('=upper.text');
    expect(renamed.steps!.b.after).toEqual(['upper']);
    expect(renamed.steps!.m.in!.over).toEqual(['=upper.text', { 'a.b': '=upper.text' }]);
    expect(renamed.steps!.m.in!.do.in.x).toBe('=upper.text');
    expect(renamed.steps!.m.in!.do.use.outputs.text).toBe('=a.text');
    expect(rename(tiny, 'inputs', 'text', 'word', report).steps?.a.in?.text).toBe('=inputs.word');
    expect(rename(tiny, 'outputs', 'text', 'word', report).outputs).toEqual({ word: '=b.text' });
    const positioned = set(tiny, ['view', 'positions', handle(['steps', 'a'])], { x: 10, y: 20 });
    expect(rename(positioned, 'steps', 'a', 'c', report).view?.positions).toEqual({ [handle(['steps', 'c'])]: { x: 10, y: 20 } });
  });
});

describe('document to graph', () => {
  it('uses only reported references and draws after as a different line', () => {
    const flow = set(tiny, ['steps', 'b', 'in', 'data'], { word: '=a.text' });
    const result = graph(flow, catalog, undefined, report);
    expect(result.nodes.map(node => parseHandle(node.id))).toEqual([['inputs'], ['steps', 'a'], ['steps', 'b'], ['outputs']]);
    expect(result.edges.map(edge => edge.className)).toEqual(['', '', '', 'after']);
    expect(result.edges).toHaveLength(4); // An unreported string is never guessed to be a reference.
    const nested = { ...refs[1], path: ['steps', 'b', 'in', 'data', 'word'] };
    expect(parseHandle(graph(flow, catalog, undefined, { ...report, references: [nested] }).edges[0].targetHandle)).toEqual(nested.path);
  });
  it('marks bad and unchecked lines from the entries themselves', () => {
    expect(graph(tiny, catalog, undefined, { ...report, references: [{ ...refs[1], bad: true }] }).edges[0].className).toBe('bad');
    expect(graph(tiny, catalog, undefined, { ...report, references: [{ ...refs[1], unchecked: true }] }).edges[0].className).toBe('unchecked');
  });
  it('shows bound ports supplied by the checker', () => {
    const flow: Workflow = { steps: { m: { use: 'map' } } };
    const nodes = { [JSON.stringify(['steps', 'm'])]: { inputs: { over: 'list of text', do: 'node(text) gives text' }, outputs: { result: 'list of text' } } };
    const m = graph(flow, catalog, undefined, { ...emptyReport, nodes }).nodes[1];
    expect('outputs' in m.data && m.data.outputs?.result).toBe('list of text');
    expect('inputs' in m.data && m.data.inputs?.do).toBe('node(text) gives text');
  });
  it('uses recorded ports and positions without changing the snapshot', () => {
    const flow = set(tiny, ['view', 'positions', handle(['steps', 'a'])], { x: 7, y: 9 });
    const result = graph(flow, [], { node: 'tiny', state: 'done', inputs: { text: 'hi' }, outputs: {}, workflow: flow, steps: { a: { node: 'text.upper', state: 'done', inputs: { text: 'hi' }, outputs: { text: 'HI' } } } }, report);
    const a = result.nodes.find(node => node.id === handle(['steps', 'a']))!;
    expect(a.position).toEqual({ x: 7, y: 9 });
    expect('record' in a.data && a.data.record?.outputs.text).toBe('HI');
    expect('record' in result.nodes[0].data && result.nodes[0].data.record?.inputs.text).toBe('hi');
    expect('outputs' in a.data && a.data.outputs).toEqual({ text: 'any' });
    expect(flow).toEqual(set(tiny, ['view', 'positions', handle(['steps', 'a'])], { x: 7, y: 9 }));
    expect(runWorkflow({ node: 'text.upper', state: 'done', inputs: { text: 'hi' }, outputs: { text: 'HI' } }).steps?.node.in).toEqual({ text: 'hi' });
  });
  it('keeps a box type when showing a run', () => {
    const m = graph({ steps: { m: { use: 'map' } } }, catalog, { node: 'tiny', state: 'done', inputs: {}, outputs: {}, workflow: { steps: { m: { use: 'map' } } }, steps: { m: { node: 'map', state: 'done', inputs: { do: { use: 'text.upper' }, over: ['hi'] }, outputs: { result: ['HI'] } } } }).nodes[1];
    expect('inputs' in m.data && m.data.inputs?.do).toBe('node(T) gives U');
  });
  it('handles escaped text, identifiers and both port forms', () => {
    expect(parseHandle(handle(['steps', 'a"雪', 'in', 'x']))).toEqual(['steps', 'a"雪', 'in', 'x']);
    expect(handle(['x'])).not.toContain('"');
    expect(reference('=a.text')).toBe(true);
    expect(reference('==a.text')).toBe(false);
    expect(box({ type: 'node() gives T', kind: 'node' })).toBe(true);
    expect(box('node() gives T')).toBe(false);
    expect(kind({ type: 'arbitrary display text', kind: 'number' })).toBe('number');
    expect(type('number')).toBe('number');
    expect(type({ type: 'bool', default: false })).toBe('bool');
  });
});

describe('canvas building', () => {
  it('groups workflows first and searches every group', () => {
    const nodes = [...catalog, { name: 'words', doc: 'Reading', workflow: tiny }, { name: 'tool', doc: 'Reading' }];
    expect(groups(nodes, '').map(([group]) => group)).toEqual(['Your workflows', 'Other nodes', 'text']);
    expect(groups(nodes, 'READ').flatMap(([, nodes]) => nodes.map(node => node.name))).toEqual(['words', 'tool']);
    expect(groups(nodes, 'missing')).toEqual([]);
  });
  it('stores the dropped position and leaves a clicked step to layout', () => {
    const position = { x: 2, y: 3 };
    expect(add(tiny, 'text.upper', position).view?.positions[handle(['steps', 'upper'])]).toEqual(position);
    expect(add(tiny, 'text.upper').view).toBeUndefined();
  });
  it('uses one empty value rule and one accessor for the workflow description', () => {
    for (const [kind, value] of Object.entries({ text: '', number: 0, bool: false, list: [], object: {}, any: null })) {
      expect(emptyValue({ type: kind, kind })).toEqual(value);
    }
    expect(emptyValue({ type: 'number', kind: 'number', default: 2 })).toBe(2);
    expect(emptyValue({ type: 'bool', kind: 'bool', default: false })).toBe(false);
    const root = { inputs: { text: { type: 'text', kind: 'text' } } };
    expect(workflowDescription({ ...emptyReport, nodes: { null: root } })).toBe(root);
  });
  it('exposes ports using ordinary declarations and references with unique names', () => {
    const asked = expose(tiny, 'inputs', 'a', 'text', { type: 'text', kind: 'text', default: 'hi' });
    expect(asked.inputs?.text2).toEqual({ type: 'text', default: 'hi' });
    expect(asked.steps?.a.in?.text).toBe('=inputs.text2');
    expect(expose(tiny, 'inputs', 'a', 'prefix', { type: 'text', kind: 'text' }).inputs?.prefix).toEqual({ type: 'text' });
    for (const written of ['written', '=b.text', '==literal']) {
      const replaced = expose(set(tiny, ['steps', 'a', 'in', 'text'], written), 'inputs', 'a', 'text', { type: 'text' });
      expect(replaced.inputs?.text2).toEqual({ type: 'text' });
    }
    expect(expose(tiny, 'inputs', 'a', 'text', { type: 'text', default: '=plain default' }).inputs?.text2)
      .toEqual({ type: 'text', default: '=plain default' });
    const given = expose(tiny, 'outputs', 'a', 'text');
    expect(given.outputs?.text2).toBe('=a.text');
    expect(tiny.inputs?.text).toBe('text');
  });
  it('reorders and removes list values without losing references', () => {
    const list = ['hi', '=a.text', 'yo'];
    expect(move(list, 1, 0)).toEqual(['=a.text', 'hi', 'yo']);
    expect(set(move(list, 1, 0), [1], undefined)).toEqual(['=a.text', 'yo']);
    expect(list).toEqual(['hi', '=a.text', 'yo']);
  });
  it('uses checker markings and words for bad, unchecked, malformed and after connections', () => {
    const problem = { step: '(inline)/b', input: 'text', message: 'needs number, got text; supply a number value or output' };
    const checked = { ...report, problems: [problem], references: [{ ...refs[1], bad: true }] };
    expect(connectionProblem(checked, refs[1].path, '(inline)')).toBe(problem);
    expect(connectionProblem({ ...checked, references: [{ ...refs[1], bad: false, unchecked: true }] }, refs[1].path, '(inline)')).toBeUndefined();
    expect(connectionProblem({ ...checked, references: [] }, refs[1].path, '(inline)')).toBe(problem);
    const after = { ...problem, input: 'after', message: 'dependency makes a cycle' };
    expect(connectionProblem({ ...emptyReport, problems: [after] }, ['steps', 'b', 'after'], '(inline)')).toBe(after);
    const row = { ...refs[1], input: 'text.0', path: [...refs[1].path, '0'], bad: true };
    expect(connectionProblem({ ...checked, references: [row] }, row.path, '(inline)')).toBe(problem);
    const captured = { ...row, step: '(inline)/b/do', input: 'text', path: ['steps', 'b', 'in', 'do', 'in', 'text'] };
    const box = { ...problem, input: 'do', message: 'node signatures differ' };
    expect(connectionProblem({ ...checked, problems: [box], references: [captured] }, captured.path, '(inline)')).toBe(box);
    const namedAfter = { ...refs[1], input: 'after', path: ['steps', 'b', 'in', 'after'] };
    expect(connectionProblem({ ...checked, problems: [after], references: [namedAfter] }, namedAfter.path, '(inline)')).toBeUndefined();
  });
  it('keeps one undo entry per visit or drag, and clears redo after a new edit', () => {
    const initial: History = { present: tiny, past: [], future: [] }, visit = {};
    const first = history(initial, { type: 'edit', value: set(tiny, ['doc'], 'h'), group: visit });
    const typed = history(first, { type: 'edit', value: set(tiny, ['doc'], 'hi'), group: visit });
    expect(typed.past).toEqual([tiny]);
    const moved = history(typed, { type: 'edit', value: set(typed.present, ['view'], { x: 1 }), group: {} });
    expect(moved.past).toHaveLength(2);
    expect(history(moved, { type: 'undo' }).present).toEqual(typed.present);
    expect(history(history(moved, { type: 'undo' }), { type: 'redo' }).present).toEqual(moved.present);
    const undone = history(typed, { type: 'undo' });
    expect(undone.present).toBe(tiny);
    expect(history(undone, { type: 'edit', value: add(tiny, 'text.upper') }).future).toEqual([]);
    expect(history(typed, { type: 'edit', value: typed.present })).toBe(typed);
    expect(history(typed, { type: 'reset', value: tiny })).toEqual(initial);
    const newer = history(typed, { type: 'edit', value: current => set(current, ['outputs', 'other'], 'kept') });
    const field = history(newer, { type: 'edit', value: current => set(current, ['steps', 'a', 'in', 'text'], 'yo') });
    expect(field.present.outputs?.other).toBe('kept');
    expect(field.present.steps?.a.in?.text).toBe('yo');
  });
});

describe('layout', () => {
  const input = handle(['inputs']), output = handle(['outputs']);
  it('puts boundaries on either side and steps by dependency depth, including disconnected steps', () => {
    const positions = layout([input, 'a', 'b', 'c', output], [{ source: 'a', target: 'c' }], {}, { a: { height: 600 } });
    expect(positions[input].x).toBeLessThan(positions.a.x);
    expect(positions.a.x).toBe(positions.b.x);
    expect(positions.b.y).toBeGreaterThanOrEqual(positions.a.y + 600);
    expect(positions.a.x).toBeLessThan(positions.c.x);
    expect(positions.c.x).toBeLessThan(positions[output].x);
    expect(positions[input].y).toBe(positions[output].y);
  });
  it('preserves stored positions and avoids their rectangles with measured heights', () => {
    const pinned = { x: 390, y: 0 };
    const positions = layout([input, 'a', 'b', output], [], { a: pinned }, { a: { height: 900 } });
    expect(positions.a).toEqual(pinned);
    expect(positions.b.y).toBeGreaterThanOrEqual(900);
    expect(positions[input].x).toBeLessThan(pinned.x);
    expect(positions[output].x).toBeGreaterThan(pinned.x + 290);
    const moved = layout([input, 'a', 'b', output], [], { a: { x: -400, y: 20 } });
    expect(moved[input].x).toBeLessThan(-400);
  });
  it('keeps an empty workflow on one line across the available width and tolerates cycles', () => {
    const positions = layout([input, output], [], {}, {}, 1200);
    expect(positions[input]).toEqual({ x: 0, y: 0 });
    expect(positions[output]).toEqual({ x: 910, y: 0 });
    expect(Object.values(layout(['a', 'b'], [{ source: 'a', target: 'b' }, { source: 'b', target: 'a' }])).every(p => Number.isFinite(p.x))).toBe(true);
  });
});
