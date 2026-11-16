export type Values = Record<string, any>;
export type Port = string | { type: string; kind?: string; item?: Port; fields?: Record<string, Port>; default?: any; optional?: boolean };
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
export const nodeName = (use: Definition): string => typeof use === 'string' ? use : 'Inline workflow';
export const reference = (value: any): boolean => typeof value === 'string' && value.startsWith('=') && !value.startsWith('==');
export const handle = (path: Path): string => encodeURIComponent(JSON.stringify(path));
export const parseHandle = (id: string): Path => JSON.parse(decodeURIComponent(id));
export const copyText = (value: any): string => typeof value === 'string' ? value : JSON.stringify(value, null, 2) ?? '';
export const commandText = (args: string[]): string => args.map(word => !word || /[ \t\r\n\v\f'"$`\\|&;<>*?\[\]{}()!~#]/u.test(word) ? "'" + word.replaceAll("'", "'\\''") + "'" : word).join(' ');
export const fileValue = (value: any): boolean => value !== null && typeof value === 'object' && Object.hasOwn(value, '$file') && typeof value.$file === 'string';
export function fileLabel(value: any): string {
  if (!fileValue(value) || typeof value.name !== 'string' || !Number.isFinite(value.size) || value.size < 0) return copyText(value);
  const units = ['B', 'kB', 'MB', 'GB', 'TB'];
  let index = Math.min(units.length - 1, Math.floor(Math.log10(Math.max(1, value.size)) / 3));
  const rounded = (index: number) => Math.round(value.size / (1000 ** index / 10)) / 10;
  if (rounded(index) >= 1000 && index < units.length - 1) index++;
  return value.name + ' · ' + rounded(index) + ' ' + units[index];
}
export function fileInput(text: string): any {
  if (text.trim().startsWith('{')) { try { return JSON.parse(text); } catch {} }
  return text;
}
export const textPreview = (text: string): string => text.split('\n').slice(0, 8).join('\n').slice(0, 400);
export const canReference = (path: Path): boolean => path.length >= 2 && path.every(part => !String(part).includes('.'));
export const filterRuns = (runs: Run[], node: string, state: string, trigger = ''): Run[] => runs.filter(run => (!node || run.node === node) && (!state || run.state === state) && (!trigger || run.trigger === trigger));

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

export const outputPorts = (info: Description): Record<string, Port> => ({ ...Object.fromEntries(Object.keys(info.workflow?.outputs || {}).map(key => [key, 'any'])), ...info.outputs });

export function describe(use: Definition, catalog: Description[]): Description {
  if (typeof use === 'string') return catalog.find(node => node.name === use) || {};
  return { inputs: use.inputs, outputs: Object.fromEntries(Object.keys(use.outputs || {}).map(name => [name, 'any'])) };
}

export function inputs(use: Definition, values: Values | undefined, catalog: Description[]): Record<string, Port> {
  return { ...Object.fromEntries(Object.keys(values || {}).map(name => [name, 'any'])), ...describe(use, catalog).inputs };
}

export function rename(workflow: Workflow, collection: 'steps' | 'inputs' | 'outputs', renames: Record<string, string>, report: Report): Workflow {
  const pairs = Object.entries(renames), to = (name: string) => Object.hasOwn(renames, name) ? renames[name] : name;
  const at = (path: Path) => path.reduce((value: any, key) => value?.[key], workflow);
  let next = workflow;
  const relocate = (where: (name: string) => Path) => {
    const present = pairs.filter(([old]) => at(where(old)) !== undefined);
    for (const [old] of present) next = set(next, where(old), undefined);
    for (const [old, name] of present) next = set(next, where(name), at(where(old)));
  };
  relocate(name => [collection, name]);
  if (collection === 'outputs') return next;
  for (const ref of report.references) {
    const from = [...ref.from];
    const i = collection === 'inputs' && from[0] === 'inputs' ? 1 : collection === 'steps' && from[0] !== 'inputs' ? 0 : -1;
    if (i < 0 || !Object.hasOwn(renames, from[i])) continue;
    const path = collection === 'steps' && ref.path[0] === 'steps' ? [ref.path[0], to(String(ref.path[1])), ...ref.path.slice(2)] : ref.path;
    from[i] = to(from[i]);
    next = set(next, path, '=' + from.join('.'));
  }
  if (collection === 'steps') {
    for (const [id, step] of Object.entries(next.steps || {}) as [string, Step][]) if (step.after) next = set(next, ['steps', id, 'after'], step.after.map(to));
    relocate(name => ['view', 'positions', handle(['steps', name])]);
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

const after = (path: Path): boolean => path.length === 3 && path[2] === 'after';
export const created = (before: Workflow, after: Workflow): string => Object.keys(after.steps!).find(name => !before.steps?.[name])!;
export type Origin = { type: 'source' | 'target'; path: Path };
export type Candidate = { source: Path; target: Path; port: string };

// Extend a gesture from an existing endpoint: the new step's ports that could sit opposite it, in the order its card shows them.
export function candidates(name: string, info: Description, origin: Origin): Candidate[] {
  const { path } = origin, own = ['steps', name];
  if (origin.type === 'source') {
    const rests: Path[] = path.length === 1 ? [['after']] : Object.keys(info.inputs || {}).map(key => ['in', key]);
    return rests.map(rest => ({ source: path, target: [...own, ...rest], port: String(rest.at(-1)) }));
  }
  const sources: Path[] = after(path) ? [[name]] : Object.keys(outputPorts(info)).map(key => [name, key]);
  return sources.map(source => ({ source, target: path, port: String(source[1] ?? 'after') }));
}

export function extend(workflow: Workflow, use: string, position: { x: number; y: number }, origin: Origin, catalog: Description[]) {
  const added = add(workflow, use, position), name = created(workflow, added);
  return { workflow: added, name, tries: candidates(name, describe(use, catalog), origin).map(({ source, target, port }) => ({ workflow: connect(added, source, target), path: target, port })) };
}

export const quick = { width: 300, height: 320 };
// Cap the panel to the canvas, then keep it inside without moving the stored insertion point.
export function place(at: { x: number; y: number }, view: { x: number; y: number; zoom: number }, width: number, height: number) {
  const w = Math.min(quick.width, width), h = Math.min(quick.height, height);
  return { left: Math.max(0, Math.min(at.x * view.zoom + view.x, width - w)), top: Math.max(0, Math.min(at.y * view.zoom + view.y, height - h)), width: w, maxHeight: h };
}

export function connect(workflow: Workflow, source: Path, target: Path): Workflow {
  if (after(target)) return set(workflow, target, [...(workflow.steps?.[String(target[1])]?.after || []), source[0]]);
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

export function group(workflow: Workflow, names: string[], report: Report): { workflow: Workflow; name: string } {
  const name = unique(workflow.steps, 'node');
  let inner: Workflow = { inputs: {}, steps: Object.fromEntries(names.map(id => [id, workflow.steps![id]])), outputs: {} };
  let next = workflow;
  let supplied: Values = {};
  const incoming = new Map<string, string>(), outgoing = new Map<string, string>();
  for (const ref of report.references) {
    const inside = ref.path[0] === 'steps' && names.includes(String(ref.path[1]));
    if (inside === names.includes(ref.from[0])) continue;
    const from = ref.from.slice(0, 2), key = handle(from), ports = inside ? 'inputs' : 'outputs', shared = inside ? incoming : outgoing;
    let port = shared.get(key);
    if (!port) {
      port = unique(inner[ports], from[1]); shared.set(key, port);
      if (inside) {
        const source = from[0] === 'inputs' ? workflowDescription(report)?.inputs || workflow.inputs : report.nodes[JSON.stringify(['steps', from[0]])]?.outputs;
        inner = set(inner, ['inputs', port], type(source?.[from[1]]));
        supplied = set(supplied, [port], '=' + from.join('.'));
      } else inner = set(inner, ['outputs', port], '=' + from.join('.'));
    }
    const value = '=' + [inside ? 'inputs' : name, port, ...ref.from.slice(2)].join('.');
    if (inside) inner = set(inner, ref.path, value);
    else next = set(next, ref.path, value);
  }
  const after = [...new Set(names.flatMap(id => workflow.steps![id].after || []).filter(id => !names.includes(id)))];
  for (const [id, step] of Object.entries(workflow.steps || {})) {
    if (names.includes(id)) {
      if (step.after) inner = set(inner, ['steps', id, 'after'], step.after.filter(id => names.includes(id)));
      next = set(next, ['steps', id], undefined);
      next = set(next, ['view', 'positions', handle(['steps', id])], undefined);
    } else if (step.after) next = set(next, ['steps', id, 'after'], [...new Set(step.after.map(id => names.includes(id) ? name : id))]);
  }
  const nodes = graph(workflow, [], undefined, report).nodes.filter(node => parseHandle(node.id)[0] === 'steps' && names.includes(String(parseHandle(node.id)[1])));
  const left = Math.min(...nodes.map(node => node.position.x)), top = Math.min(...nodes.map(node => node.position.y));
  for (const node of nodes) inner = set(inner, ['view', 'positions', node.id], { x: node.position.x - left, y: node.position.y - top });
  next = set(next, ['steps', name], { use: inner, in: supplied, ...(after.length ? { after } : {}) });
  const size = graph({ steps: { [name]: next.steps![name] } }, []).nodes[1].measured;
  next = set(next, ['view', 'positions', handle(['steps', name])], {
    x: (left + Math.max(...nodes.map(node => node.position.x + node.measured.width)) - size.width) / 2,
    y: (top + Math.max(...nodes.map(node => node.position.y + node.measured.height)) - size.height) / 2,
  });
  return { workflow: next, name };
}

export function copy(workflow: Workflow, names: string[], report: Report, positions: Record<string, { x: number; y: number }>): string {
  let inner: Workflow = { steps: Object.fromEntries(names.map(id => [id, workflow.steps![id]])) };
  for (const ref of [...report.references].reverse()) {
    if (ref.path[0] === 'steps' && names.includes(String(ref.path[1])) && !names.includes(ref.from[0])) inner = set(inner, ref.path, undefined);
  }
  for (const id of names) if (inner.steps![id].after) inner = set(inner, ['steps', id, 'after'], inner.steps![id].after!.filter(other => names.includes(other)));
  return JSON.stringify({ ...inner, view: { positions: Object.fromEntries(names.map(id => [handle(['steps', id]), positions[handle(['steps', id])]])) } });
}

export function clipboard(text: string): Workflow | undefined {
  let clip: any;
  try { clip = JSON.parse(text); } catch { return; }
  const steps = clip?.steps;
  if (!steps || typeof steps !== 'object' || Array.isArray(steps) || !Object.keys(steps).length) return;
  if (Object.values(steps).some((step: any) => !step || typeof step !== 'object' || step.use == null)) throw new Error('Pasted steps need to be objects with a use.');
  const positions = Object.entries(clip.view?.positions || {}).filter(([, spot]: any) => Number.isFinite(spot?.x) && Number.isFinite(spot?.y));
  return { steps, view: { positions: Object.fromEntries(positions) } };
}

export function paste(workflow: Workflow, clip: Workflow, report: Report, at?: { x: number; y: number }): { workflow: Workflow; names: string[] } {
  const taken: Values = Object.assign(Object.create(null), workflow.steps), renames: Record<string, string> = Object.create(null);
  for (const id of Object.keys(clip.steps!)) { renames[id] = unique(taken, id); taken[renames[id]] = true; }
  const spots = Object.fromEntries(graph(clip, [], undefined, report).nodes.map(node => [node.id, node.position]));
  const spot = (id: string) => spots[handle(['steps', id])];
  const left = Math.min(...Object.keys(renames).map(id => spot(id).x)), top = Math.min(...Object.keys(renames).map(id => spot(id).y));
  const to = at ?? { x: left + cardGeometry.gap, y: top + cardGeometry.gap };
  const renamed = rename({ ...clip, view: undefined }, 'steps', renames, report).steps!;
  let next = workflow;
  for (const [old, id] of Object.entries(renames)) {
    next = set(set(next, ['steps', id], renamed[id]), ['view', 'positions', handle(['steps', id])], { x: spot(old).x - left + to.x, y: spot(old).y - top + to.y });
  }
  return { workflow: next, names: Object.values(renames) };
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

export function connectionProblem(report: Report, path: Path | undefined, scope: string): Problem | undefined {
  if (!path) return report.problems[0];
  const step = path[0] === 'steps';
  const after = step && path[2] === 'after';
  const input = step ? String(after ? 'after' : path[3]) : path.slice(0, 2).join('.');
  if (!after && report.references.some(ref => !ref.bad && JSON.stringify(ref.path) === JSON.stringify(path))) return;
  return problems(report, step ? scope + '/' + path[1] : scope, input)[0];
}

export const cardGeometry = { width: 190, open: 310, header: 26, name: 20, row: 24, border: 1, padding: 16, socket: 18, gap: 32, column: 72 };

export function reveal(box: { left: number; right: number; top: number; bottom: number }, frame: { left: number; right: number; top: number; bottom: number }) {
  const shift = (start: number, end: number, low: number, high: number) => Math.max(low + cardGeometry.padding - start, Math.min(0, high - cardGeometry.padding - end));
  return { x: shift(box.left, box.right, frame.left, frame.right), y: shift(box.top, box.bottom, frame.top, frame.bottom) };
}

export function layout(
  ids: string[], edges: { source: string; target: string }[],
  stored: Record<string, { x: number; y: number }> = {},
  sizes: Record<string, { width?: number; height?: number }> = {}, width = 0,
) {
  const input = handle(['inputs']), output = handle(['outputs']);
  const { width: cardWidth, gap } = cardGeometry, column = cardWidth + cardGeometry.column;
  const height = (id: string) => sizes[id]?.height || cardGeometry.header + cardGeometry.name + 2 * (cardGeometry.row + cardGeometry.border);
  const steps = ids.filter(id => id !== input && id !== output);
  const depth = (id: string, seen: string[] = []): number => seen.includes(id) ? 0 : Math.max(1, ...edges
    .filter(edge => edge.target === id && steps.includes(edge.source))
    .map(edge => 1 + depth(edge.source, [...seen, id])));
  const columns = Object.fromEntries(steps.map(id => [id, depth(id)]));
  columns[input] = 0; columns[output] = Math.max(0, ...Object.values(columns)) + 1;
  const positions = Object.fromEntries(ids.filter(id => stored[id]).map(id => [id, stored[id]]));
  const left = stored[input]?.x ?? Math.min(0, ...steps.filter(id => stored[id]).map(id => stored[id].x - column));
  const right = Math.max(left + columns[output] * column, ...steps.filter(id => stored[id]).map(id => stored[id].x + column), steps.length ? 0 : width - cardWidth);
  for (const id of [...ids].sort((a, b) => columns[a] - columns[b])) {
    if (stored[id]) continue;
    const x = id === output ? right : left + columns[id] * column;
    let y = 0, collision;
    while ((collision = ids.find(other => positions[other]
      && x < positions[other].x + (sizes[other]?.width || cardWidth) + gap
      && x + (sizes[id]?.width || cardWidth) + gap > positions[other].x
      && y < positions[other].y + height(other) + gap
      && y + height(id) + gap > positions[other].y))) {
      y = positions[collision].y + height(collision) + gap;
    }
    positions[id] = { x, y };
  }
  return { positions, depths: columns };
}

export function problems(report: Report, where: string, input?: string): Problem[] {
  return report.problems.filter(problem => input === undefined
    ? problem.step === where || problem.step.startsWith(where + '/')
    : problem.step === where && (problem.input === input || problem.input.startsWith(input + '.'))
      || problem.step === where + '/' + input || problem.step.startsWith(where + '/' + input + '/'));
}

export function graph(workflow: Workflow, catalog: Description[], record?: Run, report: Report = emptyReport, scope = '(inline)', width = 0) {
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
    { id: handle(['inputs']), type: 'step', position: { x: 0, y: 0 }, data: { kind: 'inputs', ports: Object.fromEntries(Object.entries(workflow.inputs || {}).map(([name, port]) => [name, workflowDescription(report)?.inputs?.[name] || port])), record } },
    ...Object.entries(workflow.steps || {}).map(([name, step]) => {
      const info = report.nodes[JSON.stringify(['steps', name])] || describe(records[name]?.workflow || step.use, catalog);
      const returned = records[name]?.outputs;
      const outputs: Record<string, Port> = { ...Object.fromEntries(Object.keys(returned || {}).map(key => [key, 'any'])), ...outputPorts(info) };
      for (const edge of edges.filter(edge => edge.source === handle(['steps', name]) && edge.className.split(' ')[0] !== 'after')) {
        const output = parseHandle(edge.sourceHandle)[1];
        if (output !== undefined) outputs[output] ||= 'any';
      }
      return { id: handle(['steps', name]), type: 'step', position: { x: 0, y: 0 }, data: {
        kind: 'step', name, step, info, outputs, record: records[name],
        inputs: { ...Object.fromEntries(Object.keys(records[name]?.inputs || {}).map(key => [key, 'any'])), ...inputs(step.use, step.in, catalog), ...info.inputs },
      } };
    }),
    { id: handle(['outputs']), type: 'step', position: { x: 0, y: 0 }, data: { kind: 'outputs', ports: workflow.outputs || {}, record } },
  ];
  const sizes = Object.fromEntries(nodes.map(node => [node.id, { width: cardGeometry.width,
    height: cardGeometry.header + 2 * cardGeometry.border + ('ports' in node.data ? Object.keys(node.data.ports).length * cardGeometry.row
      : cardGeometry.name + (Object.keys(node.data.inputs).length + Object.keys(node.data.outputs).length) * cardGeometry.row) }]));
  const { positions, depths } = layout(nodes.map(node => node.id), edges, workflow.view?.positions, sizes, width);
  return { nodes: nodes.map(node => ({ ...node, measured: sizes[node.id], style: { ...sizes[node.id], '--depth': depths[node.id] }, position: positions[node.id] })), edges: edges.map(edge => {
    const source = nodes.find(node => node.id === edge.source)?.data;
    const target = nodes.find(node => node.id === edge.target)?.data;
    const ports = source && ('outputs' in source ? source.outputs : source.ports);
    const port = ports?.[String(parseHandle(edge.sourceHandle)[1])];
    return { ...edge, style: { '--type-color': `var(--type-${kind(port) || 'any'}, var(--type-any))` },
      className: edge.className + (target?.kind === 'step' && target.record?.state === 'running' ? ' running' : '')
        + (source?.record?.state === 'done' ? ' settled' : '') };
  }) };
}

export function runWorkflow(run: Run, catalog: Description[]): Workflow {
  return run.workflow || { steps: { node: { use: run.node } }, outputs: Object.fromEntries(Object.keys({ ...describe(run.node, catalog).outputs, ...run.outputs }).map(name => [name, '=node.' + name])) };
}

const shortcutTable: Record<string, [string, string, string][]> = {
  'Move around': [
    ['pan', 'Space drag / Middle drag', 'Pan'], ['scroll', 'scroll / Shift scroll', 'Pan, or pan sideways'], ['pinch', 'Mod scroll / pinch', 'Zoom'],
    ['zoomIn', '+ / =', 'Zoom in'], ['zoomOut', '-', 'Zoom out'], ['fit', 'Shift Digit1', 'Fit the whole workflow'], ['fitSelected', 'Shift Digit2', 'Fit the selected steps'],
  ],
  'Select and edit': [
    ['all', 'Mod A', 'Select all steps'], ['close', 'Escape', 'Close the open step or clear the selection'], ['delete', 'Delete / Backspace', 'Delete the selected steps or connection'],
    ['rename', 'F2', 'Rename the selected step'], ['copy', 'Mod C', 'Copy the selected steps'], ['cut', 'Mod X', 'Cut the selected steps'], ['paste', 'Mod V', 'Paste steps'],
    ['group', 'Mod G', 'Make a node'], ['undo', 'Mod Z', 'Undo'], ['redo', 'Mod Shift Z / Mod Y', 'Redo'],
  ],
  'Find and run': [['find', '/', 'Add a step here'], ['save', 'Mod S', 'Save'], ['run', 'Mod Enter', 'Run'], ['help', '?', 'Show the shortcuts']],
};
export const shortcuts = Object.entries(shortcutTable).flatMap(([group, rows]) => rows.map(([id, keys, label]) => ({ id, group, label, keys: keys.split(' / ') })));
export const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);
const typing = ['close', 'save', 'run'];
type KeyEvent = Pick<KeyboardEvent, 'key' | 'code' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'altKey'>;

export function shortcut(event: KeyEvent, typed = false, mac = isMac): string | undefined {
  const [modifier, other] = mac ? [event.metaKey, event.ctrlKey] : [event.ctrlKey, event.metaKey];
  return shortcuts.find(row => (!typed || typing.includes(row.id)) && row.keys.some(keys => {
    const parts = keys.split(' '), main = parts.pop()!;
    return modifier === parts.includes('Mod') && !other && !event.altKey && (/^\W$/.test(main) || event.shiftKey === parts.includes('Shift'))
      && (main.startsWith('Digit') ? event.code === main : event.key.toLowerCase() === main.toLowerCase());
  }))?.id;
}

export const keysText = (row: { keys: string[] }, mac = isMac): string => row.keys.map(keys => keys.split(' ').map(part => part === 'Mod' ? mac ? '⌘' : 'Ctrl' : part.replace('Digit', '')).join('+')).join(' / ');
export const tip = (id: string, label?: string, mac = isMac): string => { const row = shortcuts.find(row => row.id === id)!; return (label ?? row.label) + ' (' + keysText(row, mac) + ')'; };
