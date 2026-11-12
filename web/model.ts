export type Values = Record<string, any>;
export type Port = string | { type: string; kind?: string; item?: Port; fields?: Record<string, Port>; default?: any };
export type Definition = string | Workflow;
export type Step = { use: Definition; in?: Values; after?: string[] };
export type Workflow = { doc?: string; inputs?: Record<string, Port>; steps?: Record<string, Step>; outputs?: Values; view?: Values };
export type Description = { name?: string; doc?: string; trigger?: boolean; inputs?: Record<string, Port>; outputs?: Record<string, Port>; workflow?: Workflow; version?: string };
export type Source = { run: string; step: string; inputs: Record<string, string[]> };
export type Run = { id?: string; node: string; unsaved?: boolean; trigger?: string; source?: Source; created?: string; started?: string; finished?: string; state: string; inputs: Values; outputs: Values; steps?: Record<string, Run>; workflow?: Workflow; error?: string };
export type Problem = { step: string; input: string; message: string };
export type Reference = { step: string; input: string; from: string[]; path: Path; bad?: boolean; unchecked?: boolean };
export type Report = { problems: Problem[]; unchecked: Problem[]; references: Reference[]; nodes: Record<string, Description> };
export const emptyReport: Report = { problems: [], unchecked: [], references: [], nodes: {} };
export type Path = (string | number)[];
export const content = (workflow: Workflow | null): string => JSON.stringify(workflow && { ...workflow, view: undefined });

export const type = (port?: Port): string => typeof port === 'string' ? port : port?.type || 'any';
export const kind = (port?: Port): string | undefined => typeof port === 'object' ? port.kind : undefined;
export const emptyValue = (port?: Port): any => (typeof port === 'object' ? port.default : undefined)
  ?? (({ text: '', number: 0, bool: false, list: [], object: {} } as Values)[kind(port) || ''] ?? null);
export const workflowDescription = (report: Report): Description | undefined => report.nodes[JSON.stringify(null)];
export const box = (port?: Port): boolean => kind(port) === 'node';
export const reference = (value: any): boolean => typeof value === 'string' && value.startsWith('=') && !value.startsWith('==');
export const handle = (path: Path): string => encodeURIComponent(JSON.stringify(path));
export const parseHandle = (id: string): Path => JSON.parse(decodeURIComponent(id));
export const copyText = (value: any): string => typeof value === 'string' ? value : JSON.stringify(value, null, 2) ?? '';
export const textPreview = (text: string): string => text.split('\n').slice(0, 8).join('\n').slice(0, 400);
export const canReference = (path: Path): boolean => path.length >= 2 && path.every(part => !String(part).includes('.'));
export const filterRuns = (runs: Run[], node: string, state: string): Run[] => runs.filter(run => (!node || run.node === node) && (!state || run.state === state));

export function runTrail(run: Run, path: string[]) {
  const trail = [{ run, label: run.node, path: [] as string[] }];
  for (const key of path) {
    const parent = trail.at(-1)!;
    trail.push({ run: parent.run.steps![key], label: parent.run.workflow ? key : 'call ' + key, path: [...parent.path, key] });
  }
  return trail;
}

export function duration(run: Run, now = Date.now()): string {
  if (!run.started) return '—';
  const ms = Math.max(0, (run.finished ? Date.parse(run.finished) : now) - Date.parse(run.started));
  return ms < 1 ? '<1 ms' : ms < 1000 ? Math.round(ms) + ' ms' : (ms / 1000).toFixed(1) + ' s';
}

export function inputValues(workflow: Workflow, values: Values = {}): Values {
  return Object.fromEntries(Object.keys(workflow.inputs || {}).filter(name => Object.hasOwn(values, name)).map(name => [name, values[name]]));
}

export function runMessages(message: string, inputs: string[]): { input?: string; message: string }[] {
  return message.split('\n').filter(Boolean).map(message => ({ message, input: inputs.find(name => {
    const quoted = JSON.stringify(name);
    return message.includes('input ' + quoted + ':') || message.includes('input ' + quoted.slice(0, -1) + '.');
  }) }));
}

export function set(value: any, path: Path, next: any): any {
  if (!path.length) return typeof next === 'function' ? next(value) : next;
  const [key, ...rest] = path;
  const item = set(value?.[key], rest, next);
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

export function unique(values: object | undefined, name: string): string {
  let result = name, n = 2;
  while (Object.hasOwn(values || {}, result)) result = name + n++;
  return result;
}

export function add(workflow: Workflow, use: string, position?: { x: number; y: number }): Workflow {
  const name = unique(workflow.steps, use.split('.').at(-1)!);
  let next = set(workflow, ['steps', name], { use });
  if (position) next = set(next, ['view', 'positions', handle(['steps', name])], position);
  return next;
}

export function connect(workflow: Workflow, source: Path, target: Path): Workflow {
  if (target.length === 3 && target[2] === 'after') return set(workflow, target, [...(workflow.steps?.[String(target[1])]?.after || []), source[0]]);
  return set(workflow, target, '=' + source.join('.'));
}

export function expose(workflow: Workflow, boundary: 'inputs' | 'outputs', step: string, port: string, description?: Port): Workflow {
  const name = unique(workflow[boundary], port);
  const input = boundary === 'inputs';
  const value = typeof description === 'object' ? description.default : undefined;
  const declaration = value === undefined ? { type: type(description) } : { type: type(description), default: value };
  const next = set(workflow, [boundary, name], input ? declaration : '');
  return connect(next, input ? ['inputs', name] : [step, port], input ? ['steps', step, 'in', port] : ['outputs', name]);
}

export function move(values: any[], from: number, to: number): any[] {
  const next = [...values];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

export function groups(catalog: Description[], search: string): [string, Description[]][] {
  const grouped = new Map<string, Description[]>();
  for (const node of catalog.filter(node => (node.name + ' ' + node.doc).toLowerCase().includes(search.toLowerCase()))) {
    const name = node.name!.includes('.') ? node.name!.split('.')[0] : node.workflow ? 'Your workflows' : 'Other nodes';
    grouped.set(name, [...(grouped.get(name) || []), node]);
  }
  return [...grouped].sort(([a], [b]) => a === 'Your workflows' ? -1 : b === 'Your workflows' ? 1 : a.localeCompare(b));
}

export type History = { present: Workflow; past: Workflow[]; future: Workflow[]; group?: object };
export function history(state: History, action: { type: 'edit' | 'undo' | 'redo' | 'reset'; value?: Workflow | ((current: Workflow) => Workflow); group?: object }): History {
  const value = set(state.present, [], action.value);
  if (action.type === 'reset') return { present: value, past: [], future: [] };
  if (action.type === 'edit') {
    if (JSON.stringify(state.present) === JSON.stringify(value)) return state;
    return { present: value, past: action.group && action.group === state.group ? state.past : [...state.past, state.present], future: [], group: action.group };
  }
  const undo = action.type === 'undo', from = undo ? state.past : state.future, to = undo ? state.future : state.past;
  if (!from.length) return state;
  return { present: from.at(-1)!, past: undo ? from.slice(0, -1) : [...to, state.present], future: undo ? [...to, state.present] : from.slice(0, -1) };
}

export function connectionProblem(report: Report, path: Path, scope: string): Problem | undefined {
  const step = path[0] === 'steps';
  const after = step && path[2] === 'after';
  const input = step ? String(after ? 'after' : path[3]) : path.slice(0, 2).join('.');
  if (!after && report.references.some(ref => !ref.bad && JSON.stringify(ref.path) === JSON.stringify(path))) return;
  return problems(report, step ? scope + '/' + path[1] : scope, input)[0];
}

export function layout(
  ids: string[], edges: { source: string; target: string }[],
  stored: Record<string, { x: number; y: number }> = {},
  sizes: Record<string, { width?: number; height?: number }> = {}, width = 0,
): Record<string, { x: number; y: number }> {
  const input = handle(['inputs']), output = handle(['outputs']);
  const steps = ids.filter(id => id !== input && id !== output);
  const depth = (id: string, seen: string[] = []): number => seen.includes(id) ? 0 : Math.max(1, ...edges
    .filter(edge => edge.target === id && steps.includes(edge.source))
    .map(edge => 1 + depth(edge.source, [...seen, id])));
  const columns = Object.fromEntries(steps.map(id => [id, depth(id)]));
  columns[input] = 0; columns[output] = Math.max(0, ...Object.values(columns)) + 1;
  const positions = Object.fromEntries(ids.filter(id => stored[id]).map(id => [id, stored[id]]));
  const left = stored[input]?.x ?? Math.min(0, ...steps.filter(id => stored[id]).map(id => stored[id].x - 390));
  const right = Math.max(left + columns[output] * 390, ...steps.filter(id => stored[id]).map(id => stored[id].x + 390), steps.length ? 0 : width - 290);
  for (const id of [...ids].sort((a, b) => columns[a] - columns[b])) {
    if (stored[id]) continue;
    const x = id === output ? right : left + columns[id] * 390;
    let y = 0, collision;
    while ((collision = ids.find(other => positions[other]
      && x < positions[other].x + (sizes[other]?.width || 290) + 32
      && x + (sizes[id]?.width || 290) + 32 > positions[other].x
      && y < positions[other].y + (sizes[other]?.height || 240) + 32
      && y + (sizes[id]?.height || 240) + 32 > positions[other].y))) {
      y = positions[collision].y + (sizes[collision]?.height || 240) + 32;
    }
    positions[id] = { x, y };
  }
  return positions;
}

export function problems(report: Report, where: string, input?: string): Problem[] {
  return report.problems.filter(problem => input === undefined
    ? problem.step === where || problem.step.startsWith(where + '/')
    : problem.step === where && (problem.input === input || problem.input.startsWith(input + '.'))
      || problem.step === where + '/' + input || problem.step.startsWith(where + '/' + input + '/'));
}

export function graph(workflow: Workflow, catalog: Description[], record?: Run, report: Report = emptyReport, scope = '(inline)', sizes: Record<string, { width?: number; height?: number }> = {}, width = 0) {
  const records = record?.workflow ? record.steps || {} : record ? { node: record } : {};
  const edges: { id: string; source: string; target: string; sourceHandle: string; targetHandle: string; className: string }[] = [];
  for (const ref of report.references) {
    const target = ref.path[0] === 'outputs' ? ['outputs'] : ['steps', ref.path[1]];
    edges.push({
      id: handle(ref.path), source: handle(ref.from[0] === 'inputs' ? ['inputs'] : ['steps', ref.from[0]]), target: handle(target),
      sourceHandle: handle(ref.from.slice(0, 2)), targetHandle: handle(ref.path),
      className: !record && ref.bad ? 'bad' : !record && ref.unchecked ? 'unchecked' : '',
    });
  }
  for (const [id, step] of Object.entries(workflow.steps || {})) {
    (step.after || []).forEach((source, i) => edges.push({
      id: handle(['steps', id, 'after', i]), source: handle(['steps', source]), target: handle(['steps', id]),
      sourceHandle: handle([source]), targetHandle: handle(['steps', id, 'after']),
      className: 'after' + (!record && problems(report, scope + '/' + id, 'after').length ? ' bad' : ''),
    }));
  }
  const nodes = [
    { id: handle(['inputs']), type: 'step', position: { x: 0, y: 0 }, data: { kind: 'inputs', ports: { ...workflow.inputs, ...workflowDescription(report)?.inputs }, record } },
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
    { id: handle(['outputs']), type: 'step', position: { x: 0, y: 0 }, data: { kind: 'outputs', ports: workflow.outputs || {}, record } },
  ];
  const positions = layout(nodes.map(node => node.id), edges, workflow.view?.positions, sizes, width);
  return { nodes: nodes.map(node => ({ ...node, measured: sizes[node.id], position: positions[node.id] })), edges };
}

export function runWorkflow(run: Run): Workflow {
  return run.workflow || { steps: { node: { use: run.node } }, outputs: Object.fromEntries(Object.keys(run.outputs || {}).map(name => [name, '=node.' + name])) };
}
