export type Values = Record<string, any>;
export type Port = string | { type: string; default?: any };
export type Definition = string | Workflow;
export type Step = { use: Definition; in?: Values; after?: string[] };
export type Workflow = { doc?: string; inputs?: Record<string, Port>; steps?: Record<string, Step>; outputs?: Values; view?: Values };
export type Description = { name?: string; doc?: string; trigger?: boolean; inputs?: Record<string, Port>; outputs?: Record<string, Port>; workflow?: Workflow; version?: string };
export type Run = { id?: string; node: string; unsaved?: boolean; trigger?: string; created?: string; state: string; inputs: Values; outputs: Values; steps?: Record<string, Run>; workflow?: Workflow; error?: string };
export type Problem = { step: string; input: string; message: string };
export type Reference = { step: string; input: string; from: string[]; path: Path; bad?: boolean; unchecked?: boolean };
export type Report = { problems: Problem[]; unchecked: Problem[]; references: Reference[]; nodes: Record<string, Description> };
export const emptyReport: Report = { problems: [], unchecked: [], references: [], nodes: {} };
export type Path = (string | number)[];
export const content = (workflow: Workflow | null): string => JSON.stringify(workflow && { ...workflow, view: undefined });

export const type = (port?: Port): string => typeof port === 'string' ? port : port?.type || 'any';
export const box = (port?: Port): boolean => type(port).startsWith('node(');
export const reference = (value: any): boolean => typeof value === 'string' && value.startsWith('=') && !value.startsWith('==');
export const handle = (path: Path): string => encodeURIComponent(JSON.stringify(path));
export const parseHandle = (id: string): Path => JSON.parse(decodeURIComponent(id));

export function set(value: any, path: Path, next: any): any {
  if (!path.length) return next;
  const [key, ...rest] = path;
  const item = rest.length ? set(value?.[key], rest, next) : next;
  const copy = Array.isArray(value) ? [...value] : { ...value, [key]: item };
  if (Array.isArray(copy)) {
    if (item === undefined) copy.splice(Number(key), 1);
    else copy[Number(key)] = item;
  } else if (item === undefined) delete copy[key];
  return copy;
}

export function describe(use: Definition, catalog: Description[]): Description {
  if (typeof use === 'string') return catalog.find(node => node.name === use) || {};
  return { inputs: use.inputs, outputs: Object.fromEntries(Object.keys(use.outputs || {}).map(name => [name, 'any'])) };
}

export function inputs(use: Definition, values: Values | undefined, catalog: Description[]): Record<string, Port> {
  return { ...Object.fromEntries(Object.keys(values || {}).map(name => [name, 'any'])), ...describe(use, catalog).inputs };
}

export function rename(workflow: Workflow, collection: 'steps' | 'inputs' | 'outputs', old: string, name: string, report: Report): Workflow {
  const value = workflow[collection]?.[old];
  let next = set(set(workflow, [collection, old], undefined), [collection, name], value);
  if (collection === 'outputs') return next;
  for (const ref of report.references) {
    const from = [...ref.from];
    const i = collection === 'inputs' && from[0] === 'inputs' ? 1 : collection === 'steps' && from[0] !== 'inputs' ? 0 : -1;
    if (i < 0 || from[i] !== old) continue;
    const path = collection === 'steps' && ref.path[0] === 'steps' && ref.path[1] === old ? [ref.path[0], name, ...ref.path.slice(2)] : ref.path;
    from[i] = name;
    next = set(next, path, '=' + from.join('.'));
  }
  if (collection === 'steps') for (const [id, step] of Object.entries(next.steps || {}) as [string, Step][]) {
    if (step.after) next = set(next, ['steps', id, 'after'], step.after.map(item => item === old ? name : item));
  }
  if (collection === 'steps' && next.view?.positions?.[handle(['steps', old])]) {
    next = set(next, ['view', 'positions', handle(['steps', name])], next.view.positions[handle(['steps', old])]);
    next = set(next, ['view', 'positions', handle(['steps', old])], undefined);
  }
  return next;
}

export function add(workflow: Workflow, use: string): Workflow {
  let n = 1;
  while (workflow.steps?.['step' + n]) n++;
  return set(workflow, ['steps', 'step' + n], { use });
}

export function connect(workflow: Workflow, source: Path, target: Path): Workflow {
  if (target.length === 3 && target[2] === 'after') return set(workflow, target, [...(workflow.steps?.[String(target[1])]?.after || []), source[0]]);
  return set(workflow, target, '=' + source.join('.'));
}

export function layout(ids: string[], edges: { source: string; target: string }[]): Record<string, { x: number; y: number }> {
  const depth = (id: string, seen: string[] = []): number => seen.includes(id) ? 0 : Math.max(0, ...edges
    .filter(edge => edge.target === id && ids.includes(edge.source))
    .map(edge => 1 + depth(edge.source, [...seen, id])));
  const rows: Record<number, number> = {};
  return Object.fromEntries(ids.map(id => {
    const column = depth(id);
    const row = rows[column] || 0;
    rows[column] = row + 1;
    return [id, { x: column * 390, y: row * 440 }];
  }));
}

export function problems(report: Report, where: string, input?: string): Problem[] {
  return report.problems.filter(problem => input === undefined
    ? problem.step === where || problem.step.startsWith(where + '/')
    : problem.step === where && (problem.input === input || problem.input.startsWith(input + '.'))
      || problem.step === where + '/' + input || problem.step.startsWith(where + '/' + input + '/'));
}

export function graph(workflow: Workflow, catalog: Description[], records: Record<string, Run> = {}, report: Report = emptyReport, scope = '(inline)') {
  const edges: { id: string; source: string; target: string; sourceHandle: string; targetHandle: string; className: string }[] = [];
  for (const ref of report.references) {
    const target = ref.path[0] === 'outputs' ? ['outputs'] : ['steps', ref.path[1]];
    edges.push({
      id: handle(ref.path), source: handle(ref.from[0] === 'inputs' ? ['inputs'] : ['steps', ref.from[0]]), target: handle(target),
      sourceHandle: handle(ref.from.slice(0, 2)), targetHandle: handle(ref.path),
      className: ref.bad ? 'bad' : ref.unchecked ? 'unchecked' : '',
    });
  }
  for (const [id, step] of Object.entries(workflow.steps || {})) {
    (step.after || []).forEach((source, i) => edges.push({
      id: handle(['steps', id, 'after', i]), source: handle(['steps', source]), target: handle(['steps', id]),
      sourceHandle: handle([source]), targetHandle: handle(['steps', id, 'after']),
      className: 'after' + (problems(report, scope + '/' + id, 'after').length ? ' bad' : ''),
    }));
  }
  const nodes = [
    { id: handle(['inputs']), type: 'step', position: { x: 0, y: 0 }, data: { kind: 'inputs', ports: workflow.inputs || {} } },
    ...Object.entries(workflow.steps || {}).map(([name, step]) => {
      const info = report.nodes[JSON.stringify(['steps', name])] || describe(records[name]?.workflow || step.use, catalog);
      const returned = records[name]?.outputs;
      const outputs: Record<string, Port> = returned
        ? Object.fromEntries(Object.keys(returned).map(key => [key, info.outputs?.[key] || 'any']))
        : { ...info.outputs, ...Object.fromEntries(Object.keys(info.workflow?.outputs || {}).map(key => [key, 'any'])) };
      for (const edge of edges.filter(edge => edge.source === handle(['steps', name]) && edge.className.split(' ')[0] !== 'after')) {
        const output = parseHandle(edge.sourceHandle)[1];
        if (output !== undefined) outputs[output] ||= 'any';
      }
      return { id: handle(['steps', name]), type: 'step', position: { x: 0, y: 0 }, data: {
        kind: 'step', name, step, info, outputs, record: records[name],
        inputs: records[name]?.inputs
          ? Object.fromEntries(Object.keys(records[name].inputs).map(key => [key, info.inputs?.[key] || 'any']))
          : { ...inputs(step.use, step.in, catalog), ...info.inputs },
      } };
    }),
    { id: handle(['outputs']), type: 'step', position: { x: 0, y: 0 }, data: { kind: 'outputs', ports: workflow.outputs || {} } },
  ];
  const positions = layout(nodes.map(node => node.id), edges);
  return { nodes: nodes.map(node => ({ ...node, position: workflow.view?.positions?.[node.id] || positions[node.id] })), edges };
}

export function runWorkflow(run: Run): Workflow {
  return run.workflow || { steps: { node: { use: run.node, in: run.inputs } }, outputs: Object.fromEntries(Object.keys(run.outputs || {}).map(name => [name, '=node.' + name])) };
}
