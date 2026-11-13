import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Background, ControlButton, Controls, Handle, Position, ReactFlow, ReactFlowProvider, getNodesBounds, getViewportForBounds, useStore, useReactFlow, useUpdateNodeInternals } from '@xyflow/react';
import { add, content, graph, handle, parseHandle, problems, rename, set, type, kind, box, inputs as nodeInputs, reference, connect, emptyReport, emptyValue, expose, move, groups, connectionProblem, unique, workflowDescription, nodeName, reveal, cardGeometry } from './model';
import { call, op } from './client';
import { Duration, Value } from './Value';
import { Icon } from './Icon';

export function useReply(operation, args, receive, fail, interval = 0) {
  useEffect(() => {
    if (!operation) return;
    let current = true, stream, timer;
    fail('');
    const read = async () => {
      stream = call(operation, ...args);
      try { for await (const value of stream) if (current) receive(value); }
      catch (error) { if (current) fail(error.message); }
      if (current && interval) timer = setTimeout(read, interval);
    };
    read();
    return () => { current = false; clearTimeout(timer); stream.return(); };
  }, [operation, ...args, receive, fail, interval]);
}

export function Dialog({ label, onClose, className, children }) {
  const ref = useRef(null);
  useEffect(() => { const dialog = ref.current; dialog.showModal(); return () => dialog.close(); }, []);
  return createPortal(<dialog ref={ref} className={className} aria-label={label} onCancel={event => { event.preventDefault(); onClose(); }}>{children}</dialog>, document.body);
}

const nodeMime = 'application/x-inode-node';

export function NodeList({ catalog, onAdd, onOpen, opening }) {
  const [search, setSearch] = useState('');
  const [detail, showDetail] = useState(null);
  const [described, setDescribed] = useState(null);
  const [error, setError] = useState('');
  useReply(detail && op.show, [detail?.name], setDescribed, setError);
  const info = described?.name === detail?.name ? described : detail;
  return <aside className="catalog" onMouseLeave={() => showDetail(null)} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) showDetail(null); }}>
    <h2>Nodes</h2><input aria-label="Find a node" placeholder="Find a node…" value={search} onChange={event => setSearch(event.target.value)} />
    <p>{onAdd ? 'Click a node to add a step, or drag it onto the canvas or a box.' : 'Inspect a node’s inputs and outputs here, or open a workflow to edit it.'}</p>
    <div className="node-list">{groups(catalog, search).map(([group, nodes]) => <section key={group}><h3>{group}</h3>{nodes.map(node =>
      <div className="node-entry" key={node.name} onMouseEnter={() => showDetail(node)} onFocus={() => showDetail(node)}>
        <button type="button" className="node-add" disabled={!onAdd} title={'Add a step using ' + node.name} draggable={!!onAdd}
          onDragStart={event => { event.dataTransfer.setData(nodeMime, node.name); event.dataTransfer.effectAllowed = 'copy'; showDetail(null); }} onClick={() => onAdd(node.name)}>
          <strong>{node.name}</strong><small>{node.doc}</small>
        </button>
        {node.workflow && onOpen && <button className="quiet node-open" disabled={opening} onClick={() => onOpen(node.name)}>Open</button>}
      </div>)}</section>)}</div>
    {detail && <div className="node-detail" role="tooltip"><strong>{detail.name}</strong><p>{detail.doc}</p>
      {['inputs', 'outputs'].map(key => <div key={key}><h4>{key === 'inputs' ? 'Inputs' : 'Outputs'}</h4>
        {Object.entries(info[key] || {}).map(([name, port]) => <div key={name}>{name}<small>{type(port)}</small></div>)}
        {!Object.keys(info[key] || {}).length && <small>{info === detail && detail.workflow ? 'Checking…' : 'None'}</small>}
      </div>)}
      {error && <p className="problem">{error}</p>}
    </div>}
  </aside>;
}

export function Field({ port = {}, value, onChange, catalog = [], path = [], socket, descriptions = {}, issues = [], references = true, onOpen, children, item = false, placeholder, required = false, closed = false }) {
  const shape = kind(port);
  const json = !['text', 'secret', 'number', 'bool', 'node'].includes(shape);
  const fallback = port?.default;
  const shownValue = value === undefined ? fallback : value;
  const [draft, setDraft] = useState('');
  const [error, setError] = useState('');
  const [opened, open] = useState(false);
  const [editing, setEditing] = useState(false);
  const [secrets, setSecrets] = useState([]);
  const control = useRef(null);
  const shown = references && typeof shownValue === 'string' && shownValue.startsWith('==') ? shownValue.slice(1) : shownValue;
  const text = shown === undefined ? '' : json ? JSON.stringify(shown, null, 2) : String(shown);
  useReply(shape === 'secret' && onChange && !closed && op['secret list'], [], setSecrets, setError);
  useEffect(() => {
    setDraft(text); setEditing(false); setError(''); control.current?.setCustomValidity('');
  }, [text, shape]);
  const clear = (value !== undefined || error) && <button type="button" className="quiet clear" aria-label={item ? 'Remove item' : undefined} title={item ? 'Remove item' : 'Clear input'} onClick={() => {
    setEditing(false); setError(''); control.current?.setCustomValidity(''); onChange(undefined);
  }}>{item ? <Icon name="close" /> : shape === 'list' ? 'Clear list' : 'Clear'}</button>;
  const drop = {
    onDragOver: event => { if (box(port) && event.dataTransfer.types.includes(nodeMime)) { event.preventDefault(); event.stopPropagation(); } },
    onDrop: event => { if (box(port) && event.dataTransfer.getData(nodeMime)) { event.preventDefault(); event.stopPropagation(); onChange({ use: event.dataTransfer.getData(nodeMime) }); } },
  };
  const structured = shape === 'object' && port.fields && (shownValue === undefined || shownValue !== null && typeof shownValue === 'object' && !Array.isArray(shownValue))
    || shape === 'list' && kind(port.item) !== 'any' && port.item && (shownValue === undefined || Array.isArray(shownValue));
  const actions = <div className="field-actions">{children}
    {shownValue === undefined && (shape === 'text' || structured && (shape === 'list' || !Object.keys(port.fields).length)) &&
      <button type="button" className="quiet" onClick={() => onChange(emptyValue(port))}>Use empty {shape}</button>}
    {clear}
  </div>;
  const handle = socket?.(path, port, closed || !onChange || !structured && !box(port));
  if (closed) return <div className={'field summary nodrag' + (references && reference(shownValue) ? ' linked' : '')} {...drop}>
    {handle}<Value value={onChange ? shown : value} port={port} compact />
  </div>;
  if (references && reference(shownValue)) return <div className={'field linked ' + (box(port) ? 'box' : '')} {...drop}>{handle}<code>{shownValue}</code>{actions}</div>;
  const child = (path, item, data, change, childSocket, props = {}) => <Field port={item} value={data} onChange={change} catalog={catalog}
    path={path} socket={childSocket} descriptions={descriptions} references={references} onOpen={onOpen} {...props} />;
  if (box(port)) {
    const asStep = value => typeof value === 'string' ? { use: value } : value;
    const step = asStep(shownValue);
    const inline = step?.use && typeof step.use === 'object';
    const edit = (next, group) => onChange(current => set(asStep(current ?? shownValue), ['use'], next), group);
    const fields = childSocket => Object.entries({ ...nodeInputs(step.use, step.in, catalog), ...descriptions[JSON.stringify(path)]?.inputs }).map(([name, port]) => <div className="port" key={name}>
      <label>{name}<small>{type(port)}</small></label>
      {child([...path, 'in', name], port, step.in?.[name], (next, group) => onChange(current => set(asStep(current ?? shownValue), ['in', name], next), group), childSocket, { placeholder: 'Given when the box is called' })}
    </div>);
    return <div className="box nodrag nowheel" {...drop}>
      {handle}<div className="box-title">Box</div>
      <select aria-label="Node in box" required={required} value={inline ? 'inline' : step?.use ? 'node:' + step.use : ''} onChange={event => onChange({
        use: event.target.value === 'inline' ? { inputs: {}, steps: {}, outputs: {} } : event.target.value.slice(5),
      })}>
        <option value="" disabled>{placeholder || 'Choose a node'}</option><option value="inline">Inline workflow</option>{catalog.map(node => <option key={node.name} value={'node:' + node.name}>{node.name}</option>)}
      </select>
      {step?.use && <div className="box-inputs">{fields(socket)}</div>}
      {inline && <button type="button" className="open-box" onClick={() => open(true)}>Open box · Steps: {Object.keys(step.use.steps || {}).length}</button>}
      {inline && opened && <Dialog className="box-editor nodrag nopan nowheel" label="Box editor" onClose={() => open(false)}>
          <div className="toolbar"><strong>Box</strong><span>Steps: {Object.keys(step.use.steps || {}).length}</span><button type="button" title="Back (Escape)" onClick={() => open(false)}>Back</button></div>
          {issues.length > 0 && <div className="report"><Messages items={issues} /></div>}
          {Object.keys(step.use.inputs || {}).length > 0 && <details className="box-captures"><summary>Box inputs</summary>{fields()}</details>}
          <Canvas workflow={step.use} catalog={catalog} onEdit={edit} onOpen={onOpen} />
      </Dialog>}
      {actions}
      {error && <p className="problem">{error}</p>}
    </div>;
  }
  if (structured) {
    const list = shape === 'list';
    const values = shownValue || (list ? [] : {});
    const entries = list ? values.map((_, i) => [i, port.item]) : Object.entries(port.fields);
    return <div className="field-fields nodrag nowheel">{handle}
      {shownValue === undefined && placeholder && <small>{placeholder}</small>}
      {entries.map(([name, item]) => <div className={'field-row ' + (list ? 'list-item' : '')} key={name}>
      {!list && <label>{name}<small>{type(item)}</small></label>}
      {child([...path, String(name)], item, values[name], (next, group) => onChange(current => set(current ?? values, [name], next), group), socket, { item: list, children: list && <>
        <button type="button" aria-label="Move item up" title="Move item up" disabled={name === 0} onClick={() => onChange(current => move(current ?? values, name, name - 1))}><Icon name="up" /></button>
        <button type="button" aria-label="Move item down" title="Move item down" disabled={name === values.length - 1} onClick={() => onChange(current => move(current ?? values, name, name + 1))}><Icon name="down" /></button>
      </> })}
    </div>)}
      {list && <button type="button" className="quiet" onClick={() => onChange(current => [...(current ?? values), emptyValue(port.item)])} title="Add item"><Icon name="add" />Add item</button>}
      {actions}
    </div>;
  }
  const read = text => json ? text === '' ? undefined : JSON.parse(text) : shape === 'number' ? text === '' ? undefined : Number(text)
    : references && text.startsWith('=') ? '=' + text : text;
  const validate = text => {
    try {
      read(text); setError('');
      return '';
    } catch (error) { setError(error.message); return error.message; }
  };
  const entry = {
    ref: control, required, value: editing ? draft : text, 'data-draft': editing && draft !== text,
    onFocus: () => { if (!editing) setDraft(text); },
    onChange: event => { setDraft(event.target.value); setEditing(true); event.target.setCustomValidity(validate(event.target.value)); },
    onBlur: event => {
      if (!editing) return;
      const error = validate(event.target.value); event.target.setCustomValidity(error); setEditing(!!error);
      if (!error) {
        const value = read(event.target.value);
        if (JSON.stringify(value) !== JSON.stringify(shownValue)) onChange(value);
      }
    },
    onKeyDown: event => {
      if (error && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z') event.stopPropagation();
      if (!json && event.key === 'Enter') event.currentTarget.blur();
    },
  };
  return <div className="field nodrag">{handle}
    {shape === 'secret' ? <select aria-label="Secret name" required={required} value={shown ?? ''} onChange={event => onChange(references && event.target.value.startsWith('=') ? '=' + event.target.value : event.target.value)}>
      <option value="" disabled>{placeholder || 'Choose a secret'}</option>
      {[...new Set([...(shown ? [shown] : []), ...secrets])].map(name => <option key={name}>{name}</option>)}
    </select> : json ? <textarea {...entry} aria-label="JSON value" placeholder={placeholder} spellCheck="false" /> :
      shape === 'bool' ? <select aria-label="bool value" required={required} value={shown === undefined ? '' : String(shown)}
        onChange={event => onChange(event.target.value === 'true')}><option value="" disabled>{placeholder || 'Choose true or false'}</option><option value="true">True</option><option value="false">False</option></select> :
      <input {...entry} aria-label={shape + ' value'} type={shape === 'number' ? 'number' : 'text'} step={shape === 'number' ? 'any' : undefined}
        placeholder={fallback === undefined ? placeholder || 'Not filled' : String(fallback)} />}
    {value === undefined && fallback !== undefined && <small>Default</small>}
    {actions}{error && <p className="problem">{error}</p>}
  </div>;
}

export function Messages({ items }) {
  return items.map((problem, i) => <p className="problem" key={i}>{problem.input && <strong>{problem.input}: </strong>}{problem.message}</p>);
}

function Step({ id, data, width, height }) {
  const { kind, name, step, info, ports, inputs, outputs, record, workflow, catalog, edit, report, scope, onOpen, notice, propose, pendingSource, wire, open, active, focus, choose, remove, ensureVisible, fresh } = data;
  const card = useRef(null), update = useUpdateNodeInternals();
  useEffect(() => { update(id); }, [id, workflow, record, report, open, update]);
  useLayoutEffect(() => { if (active) ensureVisible(card.current.querySelector(':focus') || card.current); }, [active, width, height, ensureVisible]);
  useEffect(() => {
    if (!open) return;
    const control = focus && card.current.querySelector('[data-port=' + JSON.stringify(handle(focus)) + ']')?.querySelector('input, textarea, select, button');
    (control || card.current).focus({ preventScroll: true });
    ensureVisible(control || card.current);
  }, [open, focus, ensureVisible]);
  const where = kind === 'step' ? scope + '/' + name : scope;
  const at = input => edit ? problems(report, where, input) : [];
  const message = path => notice?.path === handle(path) && <span className="socket-message" role="alert">{notice.message}</span>;
  const target = (path, port, flat = true) => <>
    {flat && report.references.filter(ref => ref.path.length > path.length && path.every((part, i) => ref.path[i] === part)).map(ref =>
      <Handle key={handle(ref.path)} type="target" position={Position.Left} id={handle(ref.path)} isConnectable={false} style={{ visibility: 'hidden' }} />)}
    <Handle type="target" position={Position.Left} data-type={port?.kind} id={handle(path)} title={edit ? 'Connect an output here' : undefined} isConnectable={!!edit && !notice?.checking}
      onClickCapture={event => { if (pendingSource) { event.stopPropagation(); wire(pendingSource, path); } }} />
    {message(path)}
  </>;
  const source = (path, port) => <><Handle type="source" position={Position.Right} data-type={port?.kind} id={handle(path)} title={edit ? 'Connect this output to an input' : undefined} isConnectable={!!edit && !notice?.checking && !pendingSource} />{message(path)}</>;
  const field = (port, value, path, children) => <div className="field-slot" data-port={handle(path)}><Field closed={!open} references={!record} port={port} value={value} onChange={edit && ((next, group) => edit(current => set(current, path, next), group))} catalog={catalog} path={path} socket={target} descriptions={report.nodes} onOpen={onOpen}
    issues={at(path.slice(kind === 'step' ? 3 : 0).join('.'))} children={children} /></div>;
  const click = path => event => { if (edit && !open && !event.target.closest('.react-flow__handle')) { event.stopPropagation(); choose({ id, field: path }); } };
  const issues = kind === 'step' ? at() : Object.keys(ports).flatMap(key => at(kind === 'inputs' ? key : 'outputs.' + key));
  const ordered = step?.after?.length || Object.values(workflow.steps || {}).some(other => other.after?.includes(name));
  const title = kind === 'step' ? name : kind === 'inputs' ? 'Inputs' : 'Outputs';
  const problem = record?.error || at('use').map(problem => problem.message).join('\n');
  return <article ref={card} tabIndex={-1} title={problem || undefined} data-after={!!ordered} data-state={record?.state} className={'step ' + (open ? 'open ' : 'closed ') + (kind === 'step' ? record?.state || '' : '') + (issues.length ? ' has-problem' : '')}>
    <header>{open && kind === 'step' ? <>
      <Named label="Step name" name={name} names={Object.keys(workflow.steps)} onChange={edit && fresh && (next => {
        if (next !== name && choose({ id: handle(['steps', next]) })) edit(current => rename(current, 'steps', name, next, report));
      })} /><button className="quiet nodrag" aria-label="Delete step" title="Delete step (Delete / Backspace)" onClick={remove}><Icon name="delete" /></button>
    </> : <strong title={problem || title}>{title}</strong>}{kind === 'step' && record && <span className="step-status"><span className={'state ' + record.state}>{record.state}</span><Duration record={record} /></span>}</header>
    <div className="step-body">
    {kind === 'step' ? <>
      <div className="node-name"><span title={nodeName(step.use)}>{nodeName(step.use)}</span><span className="after-source" title={edit ? 'Connect to after' : undefined}>{source([name])}</span>
        <span className="after-target" title={edit ? 'Run after another step' : undefined}>{target(['steps', name, 'after'])}</span></div>
      {open && <p className="node-doc">{info.doc}</p>}
      {Object.entries(inputs).map(([key, port]) => <div className={'port ' + (at(key).length ? 'has-problem' : '')} key={key} title={at(key).map(problem => problem.message).join('\n') || type(port)} onClickCapture={click(['steps', name, 'in', key])}>
        <label>{key}{open && <small>{type(port)}</small>}{!open && at(key).length > 0 && <span className="problem-mark">!</span>}</label>
        {field(port, record ? record.inputs?.[key] : step.in?.[key], ['steps', name, 'in', key], edit && open && <button className="quiet expose" disabled={notice?.checking} onClick={() => propose(current => ({ workflow: expose(current, 'inputs', name, key, port), path: ['steps', name, 'in', key] }))}>Ask for this when run</button>)}
        {open && <Messages items={at(key)} />}
      </div>)}
      {open && <Messages items={at('after')} />}
      <div className="outputs">{Object.entries(outputs).map(([key, port]) => <div className="port output" key={key} onClickCapture={click(['outputs', key])}>
        <span>{key}{open && <small>{type(port)}</small>}</span>{source([name, key], port)}
        <div className="field-slot" data-port={handle(['outputs', key])}>{open && edit ? <div className="field-actions"><button className="quiet expose" disabled={notice?.checking} onClick={() => propose(current => ({ workflow: expose(current, 'outputs', name, key), path: ['outputs', unique(current.outputs, key)], socket: [name, key] }))}>Give this back</button></div> : <Value value={record?.outputs?.[key]} port={port} compact />}</div>
      </div>)}</div>
    </> : <div className={kind === 'outputs' ? 'outputs' : undefined}>{Object.entries(ports).map(([key, port]) => <div className={'port ' + (kind === 'inputs' ? 'output' : '')} key={key} title={kind === 'inputs' && record?.source?.inputs[key] ? 'Copied from =' + record.source.inputs[key].join('.') : undefined} data-type={kind === 'outputs' ? workflowDescription(report)?.outputs?.[key]?.kind : undefined} onClickCapture={click(kind === 'inputs' ? ['inputs', key, 'default'] : ['outputs', key])}>
      {open ? <>
        <div className="row"><Named name={key} names={Object.keys(workflow[kind])} onChange={fresh && (next => { if (next !== key && choose({ id })) edit(current => rename(current, kind, key, next, report)); })} />
          <button className="nodrag" aria-label="Delete" title="Delete" onClick={() => edit(current => set(current, [kind, key], undefined))}><Icon name="delete" /></button></div>
        {kind === 'inputs' && <label>Type<Named label="Input type" name={type(port)} onChange={next => edit(current => set(current, ['inputs', key], declaration => typeof declaration === 'object' ? { ...declaration, type: next } : next))} /></label>}
      </> : <span>{key}</span>}
      {kind === 'inputs' ? <>{source(['inputs', key], port)}<div className="field-slot" data-port={handle(['inputs', key, 'default'])}>
        {open && <label>Default</label>}<Field closed={!open} port={port} value={record ? record.inputs?.[key] : workflow.inputs?.[key]?.default} onChange={edit && ((next, group) => edit(current => set(current, ['inputs', key], declaration => next === undefined ? { type: type(declaration) } : { type: type(declaration), default: set(declaration?.default, [], next) }), group))} catalog={catalog} references={false} />
      </div></> : record ? <>{target(['outputs', key])}<Value value={record.outputs?.[key]} port={workflowDescription(report)?.outputs?.[key]} compact /></> : field('any', port, ['outputs', key])}
      {open && <Messages items={at(kind === 'inputs' ? key : 'outputs.' + key)} />}
    </div>)}{open && <button className="add-port nodrag" onClick={() => edit(current => set(current, [kind, unique(current[kind], 'value')], kind === 'inputs' ? 'text' : ''))}><Icon name="add" />Add {kind === 'inputs' ? 'input' : 'output'}</button>}</div>}
    </div>
  </article>;
}
const nodeTypes = { step: Step };
const cardStyle = Object.fromEntries(Object.entries(cardGeometry).map(([name, value]) => ['--card-' + name, value + 'px']));
const fitOptions = { padding: 0.02 };
const minZoom = 0.15;

function Fit({ ready, nodes, width, height }) {
  const fitted = useRef(false);
  const { setViewport } = useReactFlow();
  useEffect(() => {
    if (!fitted.current && ready && width && height) {
      fitted.current = true;
      setViewport(getViewportForBounds(getNodesBounds(nodes), width, height, minZoom, 1, fitOptions.padding));
    }
  }, [ready, nodes, width, height, setViewport]);
  return null;
}

function Named({ name, onChange, names = [], label = 'Name' }) {
  const [draft, change] = useState(name);
  useEffect(() => change(name), [name]);
  return <input className="nodrag" aria-label={label} value={draft} data-draft={draft !== name} disabled={!onChange}
    onChange={event => { event.target.setCustomValidity(''); change(event.target.value); }}
    onBlur={event => {
      if (draft !== name && names.includes(draft)) {
        event.target.setCustomValidity('This name is already used.'); event.target.reportValidity();
      } else onChange?.(draft);
    }}
    onKeyDown={event => { if (event.key === 'Enter') event.currentTarget.blur(); }} />;
}

export function Canvas(props) {
  return <ReactFlowProvider><CanvasView {...props} /></ReactFlowProvider>;
}

function CanvasView({ workflow, catalog, onEdit, record, onFrom, onCall, onOpen, opening, onCheck, onUse, pendingSource, onConnected, scope = '(inline)' }) {
  const [selected, select] = useState({});
  const [fitRequest, requestFit] = useState(0);
  const [notice, setNotice] = useState(null);
  const canvas = useRef(null), frame = useRef(null), drag = useRef({}), latest = useRef({ workflow, onEdit }), connecting = useRef(0);
  latest.current = { workflow, onEdit };
  const flow = useReactFlow();
  useEffect(() => () => { connecting.current++; }, []);
  useEffect(() => { if (notice && !notice.checking) { const timer = setTimeout(() => setNotice(null), 6000); return () => clearTimeout(timer); } }, [notice]);
  const [checked, setChecked] = useState({ document: null, report: emptyReport });
  const [error, setError] = useState('');
  const key = content(workflow);
  const definition = useMemo(() => JSON.parse(key), [key]);
  const receive = useCallback(report => { const result = { document: key, report }; setChecked(result); onCheck?.(result); }, [key, onCheck]);
  const fail = useCallback(error => { setError(error); if (error) onCheck?.({ document: key, report: emptyReport, error }); }, [key, onCheck]);
  useReply(op.check, [definition], receive, fail);
  const report = checked.report;
  const fresh = checked.document === key;
  const id = useId();
  const records = record?.workflow ? record.steps || {} : record ? { node: record } : {};
  const width = useStore(state => state.width);
  const sizes = useStore(state => Object.fromEntries([...state.nodeLookup].map(([id, node]) => [id, node.measured])),
    (a, b) => Object.keys(a).length === Object.keys(b).length && Object.keys(a).every(id => a[id]?.width === b[id]?.width && a[id]?.height === b[id]?.height));
  const height = useStore(state => state.height);
  const derived = graph(workflow, catalog, record, report, scope, width);
  const ensureVisible = useCallback(element => {
    const shift = reveal(element.getBoundingClientRect(), canvas.current.querySelector('.drawing').getBoundingClientRect());
    if (shift.x || shift.y) { const view = flow.getViewport(); flow.setViewport({ ...view, x: view.x + shift.x, y: view.y + shift.y }); }
  }, [flow]);
  const choose = choice => {
    document.activeElement?.blur();
    const invalid = canvas.current.querySelector('.step.open :invalid');
    if (invalid) { invalid.reportValidity(); return false; }
    select(choice); return true;
  };
  useLayoutEffect(() => {
    const box = canvas.current.querySelector('.drawing').getBoundingClientRect(), view = flow.getViewport();
    if (frame.current) {
      const x = frame.current.left - box.left, y = frame.current.top - box.top;
      if (x || y) flow.setViewport({ ...view, x: view.x + x, y: view.y + y });
    }
    frame.current = box;
  });
  useEffect(() => { select({}); }, [record?.id]);
  const failed = Object.keys(records).find(name => records[name].state === 'failed');
  useEffect(() => {
    if (failed) select({ id: handle(['steps', failed]) });
  }, [failed, record?.state]);
  const selection = selected.id ? parseHandle(selected.id) : [];
  const name = selection[0] === 'steps' && selection.length === 2 ? selection[1] : '';
  const step = workflow.steps?.[name];
  const child = records[name];
  const boundary = selection.length === 1 ? selection[0] : '';
  const remove = () => { onEdit(current => set(current, selection, undefined)); select({}); };
  useEffect(() => {
    const key = event => {
      if (event.defaultPrevented || event.target.closest('dialog') !== canvas.current.closest('dialog')) return;
      if (event.key === 'Escape' && pendingSource) { onConnected(); event.preventDefault(); return; }
      if (event.key === 'Escape' && selected.id) { choose({}); event.preventDefault(); }
      if (onEdit && !event.target.closest('input, textarea, select, [contenteditable]') && ['Delete', 'Backspace'].includes(event.key)
        && (step || derived.edges.some(edge => edge.id === selected.id))) { event.preventDefault(); remove(); }
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  });
  const addStep = (use, position) => {
    const next = add(workflow, use, position);
    const name = Object.keys(next.steps).find(name => !workflow.steps?.[name]);
    if (!choose({ id: handle(['steps', name]) })) return;
    onEdit(current => add(current, use, position));
    const node = graph(next, catalog, record, report, scope, width).nodes.find(node => node.id === handle(['steps', name]));
    if (!position) flow.setCenter(node.position.x + node.measured.width / 2, node.position.y + node.measured.height / 2, { zoom: Math.min(flow.getZoom(), 1) });
  };
  const propose = async build => {
    const attempt = ++connecting.current, before = latest.current.workflow, next = build(before);
    const show = (message, checking = false) => setNotice({ path: handle(next.socket || next.path), message, checking });
    show('Checking connection…', true);
    try {
      for await (const report of call(op.check, JSON.parse(content(next.workflow)))) {
        if (attempt !== connecting.current) return;
        if (content(before) !== content(latest.current.workflow)) { show('The workflow changed; connect again.'); return; }
        const problem = connectionProblem(report, next.path, scope);
        if (problem) show(problem.message);
        else { setNotice(null); latest.current.onEdit(current => ({ ...next.workflow, view: current.view })); return true; }
      }
    } catch (error) { show(error.message); }
  };
  const wire = (source, target) => propose(current => ({ workflow: connect(current, source, target), path: target })).then(accepted => { if (accepted) onConnected?.(); });
  const nodes = derived.nodes.map(node => ({ ...node, measured: sizes[node.id] || node.measured,
    style: { ...node.style, ...(onEdit && node.id === selected.id ? { width: cardGeometry.open, height: 'auto' } : {}) }, selected: node.id === selected.id, data: { ...node.data, workflow, catalog, edit: onEdit, report, scope, onOpen, notice, propose, pendingSource, wire, fresh,
    active: node.id === selected.id, open: !!onEdit && node.id === selected.id, focus: selected.field, choose, remove, ensureVisible } }));
  return <div className="canvas-layout" ref={canvas} style={cardStyle} data-editable={onEdit ? '' : undefined}>
    {onOpen && <NodeList catalog={catalog} onAdd={onEdit && addStep} onOpen={onOpen} opening={opening} />}
    <div className="canvas-main">
    {!record && error && <p className="notice problem">{error}</p>}
    {pendingSource && <div className="notice use-value">Use <code>={pendingSource.join('.')}</code> · Choose an input to connect.<button onClick={onConnected}>Cancel (Escape)</button></div>}
    <div className="canvas">
      <div className="drawing" onDragOver={event => { if (onEdit && event.dataTransfer.types.includes(nodeMime)) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; } }}
        onDrop={event => { const name = event.dataTransfer.getData(nodeMime); if (onEdit && name) { event.preventDefault(); addStep(name, flow.screenToFlowPosition({ x: event.clientX, y: event.clientY })); } }}>
        <ReactFlow id={id} nodes={nodes} edges={derived.edges.map(edge => ({ ...edge, selected: edge.id === selected.id }))} nodeTypes={nodeTypes} minZoom={minZoom} maxZoom={1.5}
          nodesDraggable={!!onEdit} nodesConnectable={!!onEdit} deleteKeyCode={null} connectOnClick onNodeDragStart={() => { drag.current = {}; }}
          onNodeClick={(event, node) => { if (node.id !== selected.id && !event.target.closest('.react-flow__handle')) choose({ id: node.id }); }} onPaneClick={() => choose({})}
          onNodesChange={changes => {
            latest.current.onEdit?.(current => {
              let next = current;
              for (const change of changes) if (change.type === 'position' && change.position) next = set(next, ['view', 'positions', change.id], change.position);
              return next;
            }, drag.current);
          }}
          onConnect={edge => wire(parseHandle(edge.sourceHandle), parseHandle(edge.targetHandle))}
          onEdgeClick={(_, edge) => { if (onEdit) choose({ id: edge.id }); }}>
          <Fit key={fitRequest + ':' + !!Object.keys(workflow.steps || {}).length} ready={fitRequest > 0 || fresh || !!error} nodes={derived.nodes} width={width} height={height} /><Background gap={22} size={1} />
          <Controls orientation="horizontal" showInteractive={false} showFitView={false}>
            <ControlButton className="react-flow__controls-fitview layout" title="Fit View" aria-label="Fit View" onClick={() => requestFit(value => value + 1)}>Fit</ControlButton>
            {onEdit && <ControlButton className="layout" title="Tidy layout" disabled={!Object.keys(workflow.view?.positions || {}).length} onClick={() => { if (!choose({})) return; onEdit(current => set(current, ['view', 'positions'], undefined)); requestFit(value => value + 1); }}>Tidy</ControlButton>}
            {onEdit && derived.edges.some(edge => edge.id === selected.id) && <ControlButton aria-label="Delete connection" title="Delete connection (Delete / Backspace)" onClick={remove}><Icon name="delete" /></ControlButton>}
          </Controls>
        </ReactFlow>
        {onEdit && !Object.keys(workflow.steps || {}).length && <div className="empty-canvas">Click a node on the left or drag it onto the canvas to add a step.</div>}
      </div>
      {record && selected.id && (step || boundary) && <aside className="inspector nowheel">
        <button className="quiet close" title="Close (Escape)" onClick={() => select({})}><Icon name="close" />Close</button>
        {step ? <>
          <h3>{name}</h3><p className="inspector-node">{nodeName(step.use)}</p>
          {onFrom && <button onClick={() => onFrom(name)}>Run from here</button>}
          {child && <>
            <p><span className={'state ' + child.state}>{child.state}</span> <Duration record={child} /></p>{child.error && <p className="problem">{child.error}</p>}
            <h4>Received</h4><Value value={child.inputs ?? undefined} port={{ fields: derived.nodes.find(node => node.id === selected.id)?.data.inputs }} />
            <h4>Returned</h4><Value value={child.outputs ?? undefined} port={{ fields: derived.nodes.find(node => node.id === selected.id)?.data.outputs }} path={[name]} onUse={onUse} />
            {onCall && Object.entries(child.steps || {}).map(([key, call]) => <button className="call" key={key} onClick={() => onCall([...(record.workflow ? [name] : []), key])}>
              Call {key} · {call.node} · <span className={'state ' + call.state}>{call.state}</span> · <Duration record={call} />
            </button>)}
          </>}
        </> : <><h3>{boundary === 'inputs' ? 'Inputs' : 'Outputs'}</h3><Value value={record[boundary]} port={{ fields: workflowDescription(report)?.[boundary] }} /></>}
      </aside>}
    </div>
    {!record && <details className={'report ' + (report.problems.length ? 'has-problem' : '')} open={report.problems.length > 0}>
      <summary>{!fresh ? 'Checking… · ' : ''}{report.problems.length ? report.problems.length + ' problems' : 'Checked'}{report.unchecked.length ? ' · ' + report.unchecked.length + ' unchecked' : ''}</summary>
      {[...report.problems, ...report.unchecked].map((problem, i) => <p key={i} className={i < report.problems.length ? 'problem' : 'unchecked'}>
        {problem.step}, {problem.input}: {problem.message}
      </p>)}
    </details>}
    </div>
  </div>;
}
