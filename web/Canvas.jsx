import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Background, Controls, Handle, Position, ReactFlow, ReactFlowProvider, getNodesBounds, getViewportForBounds, useStore, useReactFlow, useUpdateNodeInternals } from '@xyflow/react';
import { add, content, graph, handle, parseHandle, problems, rename, runWorkflow, set, type, kind, box, inputs as nodeInputs, reference, connect, emptyReport, emptyValue, expose, move, groups, connectionProblem, unique, workflowDescription } from './model';
import { call, op } from './client';

export function useReply(operation, args, receive, fail) {
  useEffect(() => {
    if (!operation) return;
    let current = true;
    const stream = call(operation, ...args);
    fail('');
    (async () => {
      try { for await (const value of stream) if (current) receive(value); }
      catch (error) { if (current) fail(error.message); }
    })();
    return () => { current = false; stream.return(); };
  }, [operation, ...args, receive, fail]);
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
    <p>Click a node to add a step, or drag it onto the canvas or a box.</p>
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

export function Field({ port = {}, value, onChange, catalog = [], path = [], socket, descriptions = {}, issues = [], references = true, onOpen, children, item = false, placeholder }) {
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
  useReply(shape === 'secret' && onChange && op['secret list'], [], setSecrets, setError);
  useEffect(() => {
    setDraft(text); setEditing(false); setError(''); control.current?.setCustomValidity('');
  }, [text, shape]);
  const clear = onChange && (value !== undefined || error) && <button type="button" className="quiet clear" title={item ? 'Remove item' : 'Clear input'} onClick={() => {
    setEditing(false); setError(''); control.current?.setCustomValidity(''); onChange(undefined);
  }}>{item ? '×' : shape === 'list' ? 'Clear list' : 'Clear'}</button>;
  const drop = {
    onDragOver: event => { if (onChange && box(port) && event.dataTransfer.types.includes(nodeMime)) { event.preventDefault(); event.stopPropagation(); } },
    onDrop: event => { if (onChange && box(port) && event.dataTransfer.getData(nodeMime)) { event.preventDefault(); event.stopPropagation(); onChange({ use: event.dataTransfer.getData(nodeMime) }); } },
  };
  const structured = shape === 'object' && port.fields && (shownValue === undefined || shownValue !== null && typeof shownValue === 'object' && !Array.isArray(shownValue))
    || shape === 'list' && kind(port.item) !== 'any' && port.item && (shownValue === undefined || Array.isArray(shownValue));
  const actions = onChange && <div className="field-actions">{children}
    {shownValue === undefined && (shape === 'text' || structured && (shape === 'list' || !Object.keys(port.fields).length)) &&
      <button type="button" className="quiet" onClick={() => onChange(emptyValue(port))}>Use empty {shape}</button>}
    {clear}
  </div>;
  const handle = socket?.(path, port, !onChange || !structured);
  if (references && reference(shownValue) && (onChange || box(port))) return <div className={'field linked ' + (box(port) ? 'box' : '')} {...drop}>{handle}<code>{shownValue}</code>{actions}</div>;
  const child = (path, item, data, change, childSocket, props = {}) => <Field port={item} value={data} onChange={onChange && change} catalog={catalog}
    path={path} socket={childSocket} descriptions={descriptions} references={references} onOpen={onOpen} {...props} />;
  if (box(port)) {
    const asStep = value => typeof value === 'string' ? { use: value } : value;
    const step = asStep(shownValue);
    const inline = step?.use && typeof step.use === 'object';
    const edit = onChange && ((next, group) => onChange(current => set(asStep(current ?? shownValue), ['use'], next), group));
    const fields = childSocket => Object.entries({ ...nodeInputs(step.use, step.in, catalog), ...descriptions[JSON.stringify(path)]?.inputs }).map(([name, port]) => <div className="port" key={name}>
      <label>{name}<small>{type(port)}</small></label>
      {child([...path, 'in', name], port, step.in?.[name], (next, group) => onChange(current => set(asStep(current ?? shownValue), ['in', name], next), group), childSocket, { placeholder: 'Given when the box is called' })}
    </div>);
    return <div className="box nodrag nowheel" {...drop}>
      {handle}<div className="box-title">Box</div>
      <select aria-label="Node in box" disabled={!onChange} value={inline ? 'inline' : step?.use ? 'node:' + step.use : ''} onChange={event => onChange({
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
  if (!onChange) return <div className="field">{handle}<pre className="value">{shownValue === undefined ? 'Not filled' : JSON.stringify(shownValue, null, 2)}</pre></div>;
  if (structured) {
    const list = shape === 'list';
    const values = shownValue || (list ? [] : {});
    const entries = list ? values.map((_, i) => [i, port.item]) : Object.entries(port.fields);
    return <div className="field-fields nodrag nowheel">{handle}
      {shownValue === undefined && placeholder && <small>{placeholder}</small>}
      {entries.map(([name, item]) => <div className={'field-row ' + (list ? 'list-item' : '')} key={name}>
      {!list && <label>{name}<small>{type(item)}</small></label>}
      {child([...path, String(name)], item, values[name], (next, group) => onChange(current => set(current ?? values, [name], next), group), socket, { item: list, children: list && <>
        <button type="button" title="Move item up" disabled={name === 0} onClick={() => onChange(current => move(current ?? values, name, name - 1))}>↑</button>
        <button type="button" title="Move item down" disabled={name === values.length - 1} onClick={() => onChange(current => move(current ?? values, name, name + 1))}>↓</button>
      </> })}
    </div>)}
      {list && <button type="button" className="quiet" onClick={() => onChange(current => [...(current ?? values), emptyValue(port.item)])}>Add item</button>}
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
    ref: control, value: editing ? draft : text, 'data-draft': editing && draft !== text,
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
    {shape === 'secret' ? <select aria-label="Secret name" value={shown ?? ''} onChange={event => onChange(references && event.target.value.startsWith('=') ? '=' + event.target.value : event.target.value)}>
      <option value="" disabled>{placeholder || 'Choose a secret'}</option>
      {[...new Set([...(shown ? [shown] : []), ...secrets])].map(name => <option key={name}>{name}</option>)}
    </select> : json ? <textarea {...entry} aria-label="JSON value" placeholder={placeholder} spellCheck="false" /> :
      shape === 'bool' ? <select aria-label="bool value" value={shown === undefined ? '' : String(shown)}
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

function Step({ id, data }) {
  const { kind, name, step, info, ports, inputs, outputs, record, workflow, catalog, edit, report, scope, onOpen, notice, propose } = data;
  const update = useUpdateNodeInternals();
  useEffect(() => { update(id); }, [id, workflow, record, report, update]);
  const where = kind === 'step' ? scope + '/' + name : scope;
  const message = path => notice?.path === handle(path) && <span className="socket-message" role="alert">{notice.message}</span>;
  const target = (path, port, flat = true) => <>
    {flat && !box(port) && report.references.filter(ref => ref.path.length > path.length && path.every((part, i) => ref.path[i] === part)).map(ref =>
      <Handle key={handle(ref.path)} type="target" position={Position.Left} id={handle(ref.path)} isConnectable={false} style={{ visibility: 'hidden' }} />)}
    <Handle type="target" position={Position.Left} id={handle(path)} title="Connect an output here" isConnectable={!!edit && !notice?.checking} />
    {message(path)}
  </>;
  const source = path => <><Handle type="source" position={Position.Right} id={handle(path)} title="Connect this output to an input" isConnectable={!!edit && !notice?.checking} />{message(path)}</>;
  const field = (port, value, path, children) => <Field port={port} value={value} onChange={edit && ((next, group) => edit(current => set(current, path, next), group))} catalog={catalog} path={path} socket={target} descriptions={report.nodes} onOpen={onOpen}
    issues={problems(report, where, path.slice(kind === 'step' ? 3 : 0).join('.'))} children={children} />;
  const issues = kind === 'step' ? problems(report, where) : Object.keys(ports).flatMap(key => problems(report, scope, kind === 'inputs' ? key : 'outputs.' + key));
  return <article className={'step ' + (record?.state || '') + (issues.length ? ' has-problem' : '')}>
    <header><strong>{kind === 'step' ? name : kind === 'inputs' ? 'Inputs' : 'Outputs'}</strong>{record?.state && <span className="state">{record.state}</span>}</header>
    {kind === 'step' ? <>
      <div className="node-name">{typeof step.use === 'string' ? step.use : 'Inline workflow'}<span className="after-source" title="Connect to after">{source([name])}</span>
        <span className="after-target" title="Run after another step">{target(['steps', name, 'after'])}</span></div>
      <p className="node-doc">{info.doc}</p>
      {Object.entries(inputs).map(([key, port]) => <div className="port" key={key}>
        <label>{key}<small>{type(port)}</small></label>
        {field(port, record ? record.inputs?.[key] : step.in?.[key], ['steps', name, 'in', key], edit && <button className="quiet expose" disabled={notice?.checking} onClick={() => propose(current => ({ workflow: expose(current, 'inputs', name, key, port), path: ['steps', name, 'in', key] }))}>Ask for this when run</button>)}
        <Messages items={problems(report, where, key)} />
      </div>)}
      <Messages items={problems(report, where, 'after')} />
      <div className="outputs">{Object.entries(outputs).map(([key, port]) => <div className="port output" key={key}>
        <span>{key}<small>{type(port)}</small></span>{source([name, key])}
        {edit && <div className="field-actions"><button className="quiet expose" disabled={notice?.checking} onClick={() => propose(current => ({ workflow: expose(current, 'outputs', name, key), path: ['outputs', unique(current.outputs, key)], socket: [name, key] }))}>Give this back</button></div>}
        {record?.outputs && <pre>{JSON.stringify(record.outputs[key])}</pre>}
      </div>)}</div>
      <Messages items={problems(report, where, 'use')} />
    </> : Object.entries(ports).map(([key, port]) => <div className={'port ' + (kind === 'inputs' ? 'output' : '')} key={key}>
      <span>{key}<small>{kind === 'inputs' ? type(port) : ''}</small></span>
      {kind === 'inputs' ? source(['inputs', key]) : field('any', port, ['outputs', key])}
      {kind === 'inputs' && record && <><small>{record.source?.inputs[key]?.join('.')}</small><pre>{JSON.stringify(record.inputs?.[key], null, 2)}</pre></>}
      <Messages items={problems(report, scope, kind === 'inputs' ? key : 'outputs.' + key)} />
    </div>)}
  </article>;
}
const nodeTypes = { step: Step };

function Fit({ ready, nodes }) {
  const fitted = useRef(false);
  const frame = useStore(state => {
    if (!ready || !state.width || !state.height || fitted.current) return null;
    const measured = [...state.nodeLookup.values()];
    if (nodes.some(node => state.nodeLookup.get(node.id)?.data !== node.data) || !measured.length || measured.some(node => !node.measured?.width || !node.measured?.height)) return null;
    return getViewportForBounds(getNodesBounds(measured), state.width, state.height, state.minZoom, 1, 0.1);
  });
  const { setViewport } = useReactFlow();
  useEffect(() => {
    if (frame && !fitted.current) {
      fitted.current = true;
      setViewport(frame);
    }
  }, [frame, setViewport]);
  return null;
}

function Named({ name, onChange, names = [], label = 'Name' }) {
  const [draft, change] = useState(name);
  useEffect(() => change(name), [name]);
  return <input aria-label={label} value={draft} data-draft={draft !== name} disabled={!onChange}
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

function CanvasView({ workflow, catalog, onEdit, record, onFrom, onOpen, opening, onCheck, scope = '(inline)', nested = false }) {
  const [selected, select] = useState('');
  const [notice, setNotice] = useState(null);
  const canvas = useRef(null), drag = useRef({}), latest = useRef({ workflow, onEdit }), connecting = useRef(0);
  latest.current = { workflow, onEdit };
  const flow = useReactFlow();
  useEffect(() => () => { connecting.current++; }, []);
  useEffect(() => { if (notice && !notice.checking) { const timer = setTimeout(() => setNotice(null), 6000); return () => clearTimeout(timer); } }, [notice]);
  const [checked, setChecked] = useState({ document: null, report: emptyReport });
  const [error, setError] = useState('');
  const key = content(workflow);
  const definition = useMemo(() => JSON.parse(key), [key]);
  const receive = useCallback(report => { setChecked({ document: key, report }); onCheck?.(report); }, [key, onCheck]);
  useReply(op.check, [definition], receive, setError);
  const report = checked.report;
  const fresh = checked.document === key;
  const id = useId();
  const records = record?.workflow ? record.steps || {} : record ? { node: record } : {};
  const width = useStore(state => state.width);
  const sizes = useStore(state => Object.fromEntries([...state.nodeLookup].map(([id, node]) => [id, node.measured])),
    (a, b) => Object.keys(a).length === Object.keys(b).length && Object.keys(a).every(id => a[id]?.width === b[id]?.width && a[id]?.height === b[id]?.height));
  const derived = graph(workflow, catalog, record, report, scope, sizes, width);
  const selection = selected ? parseHandle(selected) : [];
  const name = selection[0] === 'steps' && selection.length === 2 ? selection[1] : '';
  const step = workflow.steps?.[name];
  const child = records[name];
  const boundary = selection.length === 1 ? selection[0] : '';
  const remove = () => { onEdit(current => set(current, selection, undefined)); select(''); };
  useEffect(() => {
    if (nested) return;
    const key = event => {
      if (event.defaultPrevented || event.target.closest('dialog') !== canvas.current.closest('dialog')) return;
      if (event.key === 'Escape' && selected) { select(''); event.preventDefault(); }
      if (onEdit && !event.target.closest('input, textarea, select, [contenteditable]') && ['Delete', 'Backspace'].includes(event.key)
        && (step || derived.edges.some(edge => edge.id === selected))) { event.preventDefault(); remove(); }
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  });
  const addStep = (use, position) => {
    const next = add(workflow, use, position);
    const name = Object.keys(next.steps).find(name => !workflow.steps?.[name]);
    onEdit(current => add(current, use, position)); select(handle(['steps', name]));
    const at = graph(next, catalog, record, report, scope, sizes, width).nodes.find(node => node.id === handle(['steps', name])).position;
    if (!position) flow.setCenter(at.x + 145, at.y + 150, { zoom: Math.min(flow.getZoom(), 1) });
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
        else { setNotice(null); latest.current.onEdit(current => ({ ...next.workflow, view: current.view })); }
      }
    } catch (error) { show(error.message); }
  };
  const nodes = derived.nodes.map(node => ({ ...node, selected: node.id === selected, data: { ...node.data, workflow, catalog, edit: onEdit, report, scope, onOpen, notice, propose } }));
  return <div className="canvas-layout" ref={canvas} data-editable={onEdit ? '' : undefined}>
    {!nested && (onEdit || onOpen) && <NodeList catalog={catalog} onAdd={onEdit && addStep} onOpen={onOpen} opening={opening} />}
    <div className="canvas-main">
    {error && <p className="notice problem">{error}</p>}
    <div className={'canvas ' + (nested ? 'nested' : '')}>
      <div className="drawing" onDragOver={event => { if (onEdit && event.dataTransfer.types.includes(nodeMime)) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; } }}
        onDrop={event => { const name = event.dataTransfer.getData(nodeMime); if (onEdit && name) { event.preventDefault(); addStep(name, flow.screenToFlowPosition({ x: event.clientX, y: event.clientY })); } }}>
        <ReactFlow id={id} nodes={nodes} edges={derived.edges.map(edge => ({ ...edge, selected: edge.id === selected }))} nodeTypes={nodeTypes} minZoom={0.15} maxZoom={1.5}
          nodesDraggable={!!onEdit} nodesConnectable={!!onEdit} deleteKeyCode={null} connectOnClick onNodeDragStart={() => { drag.current = {}; }}
          onNodeClick={(_, node) => select(node.id)} onPaneClick={() => select('')}
          onNodesChange={changes => {
            latest.current.onEdit?.(current => {
              let next = current;
              for (const change of changes) if (change.type === 'position' && change.position) next = set(next, ['view', 'positions', change.id], change.position);
              return next;
            }, drag.current);
          }}
          onConnect={edge => propose(current => ({ workflow: connect(current, parseHandle(edge.sourceHandle), parseHandle(edge.targetHandle)), path: parseHandle(edge.targetHandle) }))}
          onEdgeClick={(_, edge) => { if (onEdit) select(edge.id); }}>
          <Fit key={record?.state + ':' + !!Object.keys(workflow.steps || {}).length} ready={fresh} nodes={nodes} /><Background gap={22} size={1} /><Controls showInteractive={false} />
        </ReactFlow>
        {onEdit && !Object.keys(workflow.steps || {}).length && <div className="empty-canvas"><strong>Add your first step</strong><p>Click a node on the left, or drag it onto the canvas.</p></div>}
      </div>
      {selected && <aside className="inspector nowheel">
        <button className="quiet close" title="Close (Escape)" onClick={() => select('')}>Close</button>
        {step && <>
          <h3>{name}</h3><p className="inspector-node">{typeof step.use === 'string' ? step.use : 'Inline workflow'}</p>
          {onFrom && <button onClick={() => onFrom(name)}>Run from here</button>}
          {onEdit && <div className="row">
            <Named label="Step name" name={name} names={Object.keys(workflow.steps)} onChange={fresh && (next => {
              if (next !== name) { onEdit(current => rename(current, 'steps', name, next, report)); select(handle(['steps', next])); }
            })} />
            <button title="Delete step (Delete / Backspace)" onClick={remove}>Delete step</button>
          </div>}
          {child ? <>
            <p className={'state ' + child.state}>{child.state}</p>{child.error && <p className="problem">{child.error}</p>}
            <h4>Received</h4><pre>{JSON.stringify(child.inputs, null, 2)}</pre><h4>Returned</h4><pre>{JSON.stringify(child.outputs, null, 2)}</pre>
            {Object.entries(child.steps || {}).map(([key, call]) => <details key={key}>
              <summary>Run {key} · {call.node} · {call.state}</summary><Canvas workflow={runWorkflow(call)} catalog={catalog} record={call} nested />
            </details>)}
          </> : <p>Run this workflow to see what this step received and returned.</p>}
          {typeof step.use === 'object' && <Canvas workflow={step.use} catalog={catalog} nested />}
          <Messages items={problems(report, scope + '/' + name)} />
        </>}
        {boundary && <>
          <h3>{boundary === 'inputs' ? 'Inputs' : 'Outputs'}</h3>
          {Object.entries(workflow[boundary] || {}).map(([key, port]) => <div className="boundary" key={key}>
            {onEdit ? <div className="row">
              <Named name={key} names={Object.keys(workflow[boundary])} onChange={fresh && (next => { if (next !== key) onEdit(current => rename(current, boundary, key, next, report)); })} />
              <button title="Delete" onClick={() => onEdit(current => set(current, [boundary, key], undefined))}>×</button>
            </div> : <strong>{key}</strong>}
            {boundary === 'inputs' ? <>
              <label>Type<Named label="Input type" name={type(port)} onChange={onEdit && (next =>
                onEdit(current => set(current, [boundary, key], typeof port === 'object' ? { ...port, type: next } : next)))} /></label>
              <label>Default<Field port={workflowDescription(report)?.inputs?.[key] || port} value={port?.default} onChange={onEdit && ((next, group) =>
                onEdit(current => set(current, [boundary, key], declaration => next === undefined ? { type: type(declaration) } : { type: type(declaration), default: set(declaration?.default, [], next) }), group))} catalog={catalog} references={false} /></label>
            </> : <Field value={port} onChange={onEdit && ((next, group) => onEdit(current => set(current, [boundary, key], next), group))} catalog={catalog} />}
          </div>)}
          {onEdit && <button onClick={() => {
            let n = 1;
            while (Object.hasOwn(workflow[boundary] || {}, 'value' + n)) n++;
            onEdit(current => set(current, [boundary, 'value' + n], boundary === 'inputs' ? 'text' : ''));
          }}>Add {boundary === 'inputs' ? 'input' : 'output'}</button>}
        </>}
        {derived.edges.find(edge => edge.id === selected) && onEdit && <>
          <h3>Connection</h3><button title="Delete connection (Delete / Backspace)" onClick={remove}>Delete connection</button>
        </>}
      </aside>}
    </div>
    <details className={'report ' + (report.problems.length ? 'has-problem' : '')} open={report.problems.length > 0}>
      <summary>{!fresh ? 'Checking… · ' : ''}{report.problems.length ? report.problems.length + ' problems' : 'Checked'}{report.unchecked.length ? ' · ' + report.unchecked.length + ' unchecked' : ''}</summary>
      {[...report.problems, ...report.unchecked].map((problem, i) => <p key={i} className={i < report.problems.length ? 'problem' : 'unchecked'}>
        {problem.step}, {problem.input}: {problem.message}
      </p>)}
    </details>
    </div>
  </div>;
}
