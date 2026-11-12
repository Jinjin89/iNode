import { describe, expect, it } from 'vitest';
import { add, box, connect, emptyReport, graph, handle, parseHandle, layout, reference, rename, runWorkflow, set, type, type Workflow, type Description, type Reference } from './model';

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
  it('adds and deletes a step without repairing dangling references', () => {
    const added = add(tiny, 'text.upper');
    expect(added.steps?.step1).toEqual({ use: 'text.upper' });
    expect(tiny.steps?.step1).toBeUndefined();
    expect(add(added, 'text.upper').steps?.step2).toEqual({ use: 'text.upper' });
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
    const result = graph(flow, catalog, {}, report);
    expect(result.nodes.map(node => parseHandle(node.id))).toEqual([['inputs'], ['steps', 'a'], ['steps', 'b'], ['outputs']]);
    expect(result.edges.map(edge => edge.className)).toEqual(['', '', '', 'after']);
    expect(result.edges).toHaveLength(4); // An unreported string is never guessed to be a reference.
    const nested = { ...refs[1], path: ['steps', 'b', 'in', 'data', 'word'] };
    expect(parseHandle(graph(flow, catalog, {}, { ...report, references: [nested] }).edges[0].targetHandle)).toEqual(nested.path);
  });
  it('marks bad and unchecked lines from the entries themselves', () => {
    expect(graph(tiny, catalog, {}, { ...report, references: [{ ...refs[1], bad: true }] }).edges[0].className).toBe('bad');
    expect(graph(tiny, catalog, {}, { ...report, references: [{ ...refs[1], unchecked: true }] }).edges[0].className).toBe('unchecked');
  });
  it('shows bound ports supplied by the checker', () => {
    const flow: Workflow = { steps: { m: { use: 'map' } } };
    const nodes = { [JSON.stringify(['steps', 'm'])]: { inputs: { over: 'list of text', do: 'node(text) gives text' }, outputs: { result: 'list of text' } } };
    const m = graph(flow, catalog, {}, { ...emptyReport, nodes }).nodes[1];
    expect('outputs' in m.data && m.data.outputs?.result).toBe('list of text');
    expect('inputs' in m.data && m.data.inputs?.do).toBe('node(text) gives text');
  });
  it('uses recorded ports and positions without changing the snapshot', () => {
    const flow = set(tiny, ['view', 'positions', handle(['steps', 'a'])], { x: 7, y: 9 });
    const result = graph(flow, [], { a: { node: 'text.upper', state: 'done', inputs: { text: 'hi' }, outputs: { text: 'HI' } } }, report);
    const a = result.nodes.find(node => node.id === handle(['steps', 'a']))!;
    expect(a.position).toEqual({ x: 7, y: 9 });
    expect('record' in a.data && a.data.record?.outputs.text).toBe('HI');
    expect('outputs' in a.data && a.data.outputs).toEqual({ text: 'any' });
    expect(flow).toEqual(set(tiny, ['view', 'positions', handle(['steps', 'a'])], { x: 7, y: 9 }));
    expect(runWorkflow({ node: 'text.upper', state: 'done', inputs: { text: 'hi' }, outputs: { text: 'HI' } }).steps?.node.in).toEqual({ text: 'hi' });
  });
  it('keeps a box type when showing a run', () => {
    const m = graph({ steps: { m: { use: 'map' } } }, catalog, { m: { node: 'map', state: 'done', inputs: { do: { use: 'text.upper' }, over: ['hi'] }, outputs: { result: ['HI'] } } }).nodes[1];
    expect('inputs' in m.data && m.data.inputs?.do).toBe('node(T) gives U');
  });
  it('handles escaped text, identifiers and both port forms', () => {
    expect(parseHandle(handle(['steps', 'a"雪', 'in', 'x']))).toEqual(['steps', 'a"雪', 'in', 'x']);
    expect(handle(['x'])).not.toContain('"');
    expect(reference('=a.text')).toBe(true);
    expect(reference('==a.text')).toBe(false);
    expect(box('node() gives T')).toBe(true);
    expect(type('number')).toBe('number');
    expect(type({ type: 'bool', default: false })).toBe('bool');
  });
});

it('lays out by dependency depth and tolerates cycles while editing', () => {
  expect(layout(['a', 'b', 'c'], [{ source: 'a', target: 'c' }])).toEqual({ a: { x: 0, y: 0 }, b: { x: 0, y: 440 }, c: { x: 390, y: 0 } });
  expect(Object.values(layout(['a', 'b'], [{ source: 'a', target: 'b' }, { source: 'b', target: 'a' }])).every(p => Number.isFinite(p.x))).toBe(true);
});
