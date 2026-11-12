import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Background, Controls, Handle, Position, ReactFlow, ReactFlowProvider, getNodesBounds, getViewportForBounds, useStore, useReactFlow, useUpdateNodeInternals } from '@xyflow/react';
import { add, content, graph, handle, parseHandle, problems, rename, runWorkflow, set, type, box, inputs as nodeInputs, reference, connect, emptyReport } from './model';
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

export function NodeList({ catalog, workflow, onEdit }) {
  const [search, setSearch] = useState('');
  return <aside className="catalog"><h2>Nodes</h2><input aria-label="Find a node" placeholder="Find a node…" value={search} onChange={event => setSearch(event.target.value)} />
    <p>Choose a node to add a step.</p>
    <div className="node-list">{catalog.filter(node => (node.name + ' ' + node.doc).toLowerCase().includes(search.toLowerCase())).map(node =>
      <button type="button" disabled={!onEdit} key={node.name} onClick={() => onEdit(add(workflow, node.name))}>
        <strong>{node.name}</strong><small>{node.doc}</small>
      </button>)}
    </div>
  </aside>;
}

export function Field({ port = 'any', value, onChange, catalog = [], path = [], socket, descriptions = {}, issues = [] }) {
  const kind = type(port);
  const fallback = typeof port === 'object' ? port.default : undefined;
  const shownValue = value === undefined ? fallback : value;
  const [draft, setDraft] = useState('');
  const [error, setError] = useState('');
  const [opened, open] = useState(false);
  useEffect(() => { setDraft(shownValue === undefined ? '' : JSON.stringify(shownValue, null, 2)); setError(''); }, [shownValue]);
  const commit = () => {
    try { onChange(draft === '' ? undefined : JSON.parse(draft)); setError(''); }
    catch (error) { setError(error.message); }
  };
  const clear = onChange && value !== undefined && <button type="button" className="quiet clear" title="Clear input" onClick={() => onChange(undefined)}>×</button>;
  const handle = socket?.(path, port);
  if (reference(shownValue) && (onChange || box(port))) return <div className="field linked">{handle}<code>{shownValue}</code>{clear}</div>;
  if (box(port)) {
    const step = typeof shownValue === 'string' ? { use: shownValue } : shownValue;
    const inline = step?.use && typeof step.use === 'object';
    const edit = onChange && (next => onChange(set(step, ['use'], next)));
    const fields = socket => Object.entries({ ...nodeInputs(step.use, step.in, catalog), ...descriptions[JSON.stringify(path)]?.inputs }).map(([name, port]) => <label className="port" key={name}>
      <span>{name}<small>{type(port)}</small></span>
      <Field port={port} value={step.in?.[name]} onChange={onChange && (next => onChange(set(step, ['in', name], next)))}
        catalog={catalog} path={[...path, 'in', name]} socket={socket} descriptions={descriptions} />
    </label>);
    return <div className="box nodrag nowheel">{handle}<div className="box-title">Box {clear}</div>
      <select aria-label="Node in box" disabled={!onChange} value={inline ? 'inline' : step?.use ? 'node:' + step.use : ''} onChange={event => onChange({
        use: event.target.value === 'inline' ? { inputs: {}, steps: {}, outputs: {} } : event.target.value.slice(5), in: {},
      })}>
        <option value="" disabled>Choose a node</option><option value="inline">Inline workflow</option>{catalog.map(node => <option key={node.name} value={'node:' + node.name}>{node.name}</option>)}
      </select>
      {step?.use && <div className="box-inputs">{fields(socket)}</div>}
      {inline && <button type="button" className="open-box" onClick={() => open(true)}>Open box · Steps: {Object.keys(step.use.steps || {}).length}</button>}
      {inline && opened && createPortal(<div className="modal-backdrop nodrag nopan nowheel">
        <section className="box-editor" role="dialog" aria-label="Box editor">
          <div className="toolbar"><strong>Box</strong><span>Steps: {Object.keys(step.use.steps || {}).length}</span><button type="button" onClick={() => open(false)}>Back</button></div>
          {issues.length > 0 && <div className="report"><Messages items={issues} /></div>}
          {Object.keys(step.use.inputs || {}).length > 0 && <details className="box-captures"><summary>Box inputs</summary>{fields()}</details>}
          <div className="workspace"><NodeList catalog={catalog} workflow={step.use} onEdit={edit} />
            <section className="work"><Canvas workflow={step.use} catalog={catalog} onEdit={edit} /></section>
          </div>
        </section>
      </div>, document.body)}
      {!step?.use && onChange && <textarea aria-label="Box JSON" value={draft} onChange={event => setDraft(event.target.value)} onBlur={commit} />}
      {error && <p className="problem">{error}</p>}
    </div>;
  }
  if (!onChange) return <div className="field">{handle}<pre className="value">{shownValue === undefined ? 'Not filled' : JSON.stringify(shownValue, null, 2)}</pre></div>;
  const json = kind !== 'text' && kind !== 'number' && kind !== 'bool';
  const shown = kind === 'text' && typeof shownValue === 'string' && shownValue.startsWith('==') ? shownValue.slice(1) : shownValue;
  return <div className={'field nodrag ' + (shownValue === undefined ? 'empty' : '')}>{handle}
    {json ? <textarea aria-label="JSON value" spellCheck="false" value={draft} onChange={event => setDraft(event.target.value)} onBlur={commit} /> :
      <input aria-label={kind + ' value'} type={kind === 'bool' ? 'checkbox' : kind === 'number' ? 'number' : 'text'}
        checked={kind === 'bool' ? shownValue === true : undefined} value={kind === 'bool' ? undefined : shown ?? ''}
        placeholder={typeof port === 'object' && 'default' in port ? String(port.default) : 'Not filled'}
        onChange={event => onChange(kind === 'bool' ? event.target.checked : kind === 'number'
          ? event.target.value === '' ? undefined : event.target.valueAsNumber
          : event.target.value.startsWith('=') ? '=' + event.target.value : event.target.value)} />}
    {value === undefined && fallback !== undefined && <small>Default</small>}
    {shownValue === undefined && <button type="button" className="quiet" onClick={() => onChange(kind === 'text' ? '' : kind === 'number' ? 0 : kind === 'bool' ? false : {})}>Fill</button>}
    {clear}{error && <p className="problem">{error}</p>}
  </div>;
}

function Messages({ items }) {
  return items.map((problem, i) => <p className="problem" key={i}>{problem.input && <strong>{problem.input}: </strong>}{problem.message}</p>);
}

function Step({ id, data }) {
  const { kind, name, step, info, ports, inputs, outputs, record, workflow, catalog, edit, report, scope } = data;
  const update = useUpdateNodeInternals();
  useEffect(() => { update(id); }, [id, workflow, record, report, update]);
  const where = kind === 'step' ? scope + '/' + name : scope;
  const target = (path, port) => <>
    {!box(port) && report.references.filter(ref => ref.path.length > path.length && path.every((part, i) => ref.path[i] === part)).map(ref =>
      <Handle key={handle(ref.path)} type="target" position={Position.Left} id={handle(ref.path)} isConnectable={false} style={{ visibility: 'hidden' }} />)}
    <Handle type="target" position={Position.Left} id={handle(path)} />
  </>;
  const source = path => <Handle type="source" position={Position.Right} id={handle(path)} />;
  const field = (port, value, path) => <Field port={port} value={value} onChange={edit && (next => edit(set(workflow, path, next)))} catalog={catalog} path={path} socket={target} descriptions={report.nodes}
    issues={problems(report, where, path.slice(kind === 'step' ? 3 : 0).join('.'))} />;
  const issues = kind === 'step' ? problems(report, where) : Object.keys(ports).flatMap(key => problems(report, scope, kind === 'inputs' ? key : 'outputs.' + key));
  return <article className={'step ' + (record?.state || '') + (issues.length ? ' has-problem' : '')}>
    <header><strong>{kind === 'step' ? name : kind === 'inputs' ? 'Inputs' : 'Outputs'}</strong>{record && <span className="state">{record.state}</span>}</header>
    {kind === 'step' ? <>
      <div className="node-name">{typeof step.use === 'string' ? step.use : 'Inline workflow'}<span className="after-source" title="Connect to after">{source([name])}</span>
        <span className="after-target" title="Run after another step"><Handle type="target" position={Position.Left} id={handle(['steps', name, 'after'])} /></span></div>
      <p className="node-doc">{info.doc}</p>
      {Object.entries(inputs).map(([key, port]) => <div className="port" key={key}>
        <label>{key}<small>{type(port)}</small></label>
        {field(port, record ? record.inputs?.[key] : step.in?.[key], ['steps', name, 'in', key])}
        <Messages items={problems(report, where, key)} />
      </div>)}
      <Messages items={problems(report, where, 'after')} />
      <div className="outputs">{Object.entries(outputs).map(([key, port]) => <div className="port output" key={key}>
        <span>{key}<small>{type(port)}</small></span>{source([name, key])}
        {record?.outputs && <pre>{JSON.stringify(record.outputs[key])}</pre>}
      </div>)}</div>
      <Messages items={problems(report, where, 'use')} />
    </> : Object.entries(ports).map(([key, port]) => <div className={'port ' + (kind === 'inputs' ? 'output' : '')} key={key}>
      <span>{key}<small>{kind === 'inputs' ? type(port) : ''}</small></span>
      {kind === 'inputs' ? source(['inputs', key]) : field('any', port, ['outputs', key])}
      <Messages items={problems(report, scope, kind === 'inputs' ? key : 'outputs.' + key)} />
    </div>)}
  </article>;
}
const nodeTypes = { step: Step };

function Fit({ ready, report, workflow }) {
  const fitted = useRef('');
  const frame = useStore(state => {
    const size = state.width + ':' + state.height;
    if (!ready || !state.width || !state.height || fitted.current === size) return null;
    const measured = [...state.nodeLookup.values()];
    if (!measured.length || measured.some(node => node.data.report !== report || node.data.workflow !== workflow || !node.measured?.width || !node.measured?.height)) return null;
    return { size, viewport: getViewportForBounds(getNodesBounds(measured), state.width, state.height, state.minZoom, 1, 0.1) };
  });
  const { setViewport } = useReactFlow();
  useEffect(() => {
    if (frame && fitted.current !== frame.size) {
      fitted.current = frame.size;
      setViewport(frame.viewport);
    }
  }, [frame, setViewport]);
  return null;
}

function Named({ name, onChange, names = [], label = 'Name' }) {
  const [draft, change] = useState(name);
  useEffect(() => change(name), [name]);
  return <input aria-label={label} value={draft} disabled={!onChange}
    onChange={event => { event.target.setCustomValidity(''); change(event.target.value); }}
    onBlur={event => {
      if (draft !== name && names.includes(draft)) {
        event.target.setCustomValidity('This name is already used.'); event.target.reportValidity();
      } else onChange?.(draft);
    }}
    onKeyDown={event => { if (event.key === 'Enter') event.currentTarget.blur(); }} />;
}

export function Canvas({ workflow, catalog, onEdit, record, scope = '(inline)', nested = false }) {
  const [selected, select] = useState('');
  const [checked, setChecked] = useState({ document: null, report: emptyReport });
  const [error, setError] = useState('');
  const key = content(workflow);
  const definition = useMemo(() => JSON.parse(key), [key]);
  const receive = useCallback(report => setChecked({ document: key, report }), [key]);
  useReply(op.check, [definition], receive, setError);
  const report = checked.report;
  const fresh = checked.document === key;
  const id = useId();
  const records = record?.workflow ? record.steps || {} : record ? { node: record } : {};
  const derived = graph(workflow, catalog, records, report, scope);
  const nodes = derived.nodes.map(node => ({ ...node, selected: node.id === selected, data: { ...node.data, workflow, catalog, edit: onEdit, report, scope } }));
  const selection = selected ? parseHandle(selected) : [];
  const name = selection[0] === 'steps' && selection.length === 2 ? selection[1] : '';
  const step = workflow.steps?.[name];
  const child = records[name];
  const boundary = selection.length === 1 ? selection[0] : '';
  const remove = () => { onEdit(set(workflow, ['steps', name], undefined)); select(''); };
  return <ReactFlowProvider initialNodes={nodes} initialEdges={derived.edges}>
    {error && <p className="notice problem">{error}</p>}
    <div className={'canvas ' + (nested ? 'nested' : '')}>
      <div className="drawing">
        <ReactFlow id={id} nodes={nodes} edges={derived.edges} nodeTypes={nodeTypes} minZoom={0.15} maxZoom={1.5}
          nodesDraggable={!!onEdit} nodesConnectable={!!onEdit} deleteKeyCode={null}
          onNodeClick={(_, node) => select(node.id)} onPaneClick={() => select('')}
          onNodesChange={changes => {
            let next = workflow;
            for (const change of changes) if (change.type === 'position' && change.position) next = set(next, ['view', 'positions', change.id], change.position);
            if (next !== workflow) onEdit?.(next);
          }}
          onConnect={edge => onEdit?.(connect(workflow, parseHandle(edge.sourceHandle), parseHandle(edge.targetHandle)))}
          onEdgeClick={(_, edge) => { if (onEdit) select(edge.id); }}>
          <Fit key={record?.state} ready={fresh} report={report} workflow={workflow} /><Background gap={22} size={1} /><Controls showInteractive={false} />
        </ReactFlow>
      </div>
      {selected && <aside className="inspector nowheel">
        <button className="quiet close" onClick={() => select('')}>Close</button>
        {step && <>
          <h3>Step {name}</h3>
          {onEdit && <div className="row">
            <Named label="Step name" name={name} names={Object.keys(workflow.steps)} onChange={fresh && (next => {
              if (next !== name) { onEdit(rename(workflow, 'steps', name, next, report)); select(handle(['steps', next])); }
            })} />
            <button onClick={remove}>Delete step</button>
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
              <Named name={key} names={Object.keys(workflow[boundary])} onChange={fresh && (next => { if (next !== key) onEdit(rename(workflow, boundary, key, next, report)); })} />
              <button title="Delete" onClick={() => onEdit(set(workflow, [boundary, key], undefined))}>×</button>
            </div> : <strong>{key}</strong>}
            {boundary === 'inputs' ? <>
              <label>Type<Named label="Input type" name={type(port)} onChange={onEdit && (next =>
                onEdit(set(workflow, [boundary, key], typeof port === 'object' ? { ...port, type: next } : next)))} /></label>
              <label>Default<Field port={port} value={port?.default} onChange={onEdit && (next =>
                onEdit(set(workflow, [boundary, key], next === undefined ? { type: type(port) } : { type: type(port), default: next })))} catalog={catalog} /></label>
            </> : <Field value={port} onChange={onEdit && (next => onEdit(set(workflow, [boundary, key], next)))} catalog={catalog} />}
          </div>)}
          {onEdit && <button onClick={() => {
            let n = 1;
            while (Object.hasOwn(workflow[boundary] || {}, 'value' + n)) n++;
            onEdit(set(workflow, [boundary, 'value' + n], boundary === 'inputs' ? 'text' : ''));
          }}>Add {boundary === 'inputs' ? 'input' : 'output'}</button>}
        </>}
        {derived.edges.find(edge => edge.id === selected) && onEdit && <>
          <h3>Connection</h3><button onClick={() => { onEdit(set(workflow, selection, undefined)); select(''); }}>Delete connection</button>
        </>}
      </aside>}
    </div>
    <details className={'report ' + (report.problems.length ? 'has-problem' : '')} open={report.problems.length > 0}>
      <summary>{report.problems.length ? report.problems.length + ' problems' : 'Checked'}{report.unchecked.length ? ' · ' + report.unchecked.length + ' unchecked' : ''}</summary>
      {[...report.problems, ...report.unchecked].map((problem, i) => <p key={i} className={i < report.problems.length ? 'problem' : 'unchecked'}>
        {problem.step}, {problem.input}: {problem.message}
      </p>)}
    </details>
  </ReactFlowProvider>;
}
