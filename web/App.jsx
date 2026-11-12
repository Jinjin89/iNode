import { useEffect, useReducer, useRef, useState } from 'react';
import { Canvas, Dialog, Field, useReply } from './Canvas';
import { call, op } from './client';
import { content, emptyReport, history, runWorkflow, set, type, workflowDescription } from './model';
import { When } from './When';
import { Secrets } from './Secrets';

function Source({ source, showRun, fail }) {
  const [record, setRecord] = useState(null);
  useReply(source && op['runs show'], [source?.run], setRecord, fail);
  return source && <div className="toolbar">Source run <button className="quiet" onClick={() => showRun(source.run)}>{source.run}</button>
    <span>Step {source.step}{record?.id === source.run && <> · finished {new Date(record.finished).toLocaleString()} ({Math.floor((Date.now() - new Date(record.finished)) / 1000)} seconds ago)</>}</span>
  </div>;
}

const empty = { inputs: {}, steps: {}, outputs: {} };
const editor = ':is(.canvas-layout[data-editable], .box-editor)';

export function App() {
  const [catalog, setCatalog] = useState([]);
  const [edits, dispatch] = useReducer(history, { present: empty, past: [], future: [] });
  const workflow = edits.present;
  const setWorkflow = (value, group) => dispatch({ type: 'edit', value, group });
  const [opened, setOpened] = useState({ name: '', version: '', workflow: empty, key: 0 });
  const [record, setRecord] = useState(null);
  const [runId, setRunId] = useState('');
  const [screen, setScreen] = useState('Canvas');
  const [runs, setRuns] = useState([]);
  const [plugins, setPlugins] = useState([]);
  const [source, setSource] = useState('');
  const [manifest, setManifest] = useState('');
  const [runInputs, setRunInputs] = useState({});
  const [enterInputs, setEnterInputs] = useState(false);
  const [report, setReport] = useState(emptyReport);
  const [preview, setPreview] = useState(null);
  const [returnRun, setReturnRun] = useState('');
  const [error, setError] = useState('');
  const [pending, setPending] = useState([]);
  const [discard, setDiscard] = useState(null);
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState('');
  const latest = useRef({ workflow, opened }), saveButton = useRef(null), runButton = useRef(null), descriptionVisit = useRef({});
  latest.current = { workflow, opened, editing: screen === 'Canvas' && !runId && !enterInputs };
  const busy = pending.includes('operation');
  const unsaved = JSON.stringify(workflow) !== JSON.stringify(opened.workflow);
  const savedDefinition = !!opened.version && content(workflow) === content(opened.workflow);
  const perform = async (action, name = 'operation') => {
    setPending(previous => [...previous, name]); setError('');
    try { await action(); } catch (error) { setError(error.message); }
    finally { setPending(previous => previous.filter(item => item !== name)); }
  };
  const dirty = () => {
    const { workflow, opened, editing } = latest.current;
    return JSON.stringify(workflow) !== JSON.stringify(opened.workflow) || editing && !!document.querySelector(editor + ' :is([data-draft="true"], :invalid)');
  };
  const leave = action => {
    if (dirty()) setDiscard(() => action);
    else action();
  };
  const valid = () => {
    const input = document.querySelector(editor + ' :invalid');
    return !input || input.reportValidity();
  };
  const replace = (node, back = '') => {
    dispatch({ type: 'reset', value: node.workflow });
    setOpened(previous => ({ name: node.name, version: node.version, workflow: node.workflow, key: previous.key + 1 }));
    setRunId(''); setRecord(null); setScreen('Canvas'); setRunInputs({}); setReturnRun(back);
  };
  const open = async (name, back = '') => {
    for await (const node of call(op.show, name)) {
      leave(() => replace(node, back));
    }
  };
  useEffect(() => {
    perform(async () => {
      for await (const nodes of call(op.nodes)) {
        setCatalog(nodes);
      }
    }, 'open');
  }, []);
  useReply(runId && op['runs follow'], [runId], setRecord, setError);
  const write = name => perform(async () => {
    const snapshot = workflow, basedOn = opened;
    setNaming(false);
    for await (const node of call(op.save, name, opened.version, snapshot)) {
      if (latest.current.opened === basedOn) setOpened({ ...basedOn, name: node.name, version: node.version, workflow: snapshot });
    }
    for await (const nodes of call(op.nodes)) setCatalog(nodes);
  }, 'save');
  const save = () => { if (!valid()) return; if (opened.name) write(opened.name); else { setName(''); setNaming(true); } };
  const run = () => perform(async () => {
    setEnterInputs(false);
    for await (const id of call(op.run, savedDefinition ? opened.name : workflow, runInputs, opened.name || 'Unsaved workflow')) showRun(id);
  }, 'run');
  const changePlugin = (operation, source) => perform(async () => {
    setManifest('');
    try { for await (const text of call(operation, source)) setManifest(previous => previous + text); }
    finally {
      for (const [operation, receive] of [[op.plugins, setPlugins], [op.nodes, setCatalog]]) {
        for await (const rows of call(operation)) receive(rows);
      }
    }
  });
  const showRun = id => { setPreview(null); setReturnRun(''); setRecord(previous => previous?.id === id ? previous : null); setRunId(id); setScreen('Canvas'); };
  const from = (step, start = false) => perform(async () => {
    for await (const value of call(op['runs from'], record.id, step, start)) {
      if (start) showRun(value); else setPreview(value);
    }
  });
  useEffect(() => {
    const warn = event => { if (dirty()) { event.preventDefault(); event.returnValue = ''; } };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, []);
  useEffect(() => {
    const key = event => {
      if (event.defaultPrevented) return;
      const pressed = event.key.toLowerCase();
      if (!(event.ctrlKey || event.metaKey) || !['s', 'z', 'enter'].includes(pressed)) return;
      if (pressed === 'z' && (screen !== 'Canvas' || runId || naming || discard || preview || enterInputs)) return;
      event.preventDefault();
      if (screen !== 'Canvas' || runId || naming || discard || preview || enterInputs) return;
      document.activeElement?.blur();
      requestAnimationFrame(() => {
        if (pressed === 'z') dispatch({ type: event.shiftKey ? 'redo' : 'undo' });
        else (pressed === 's' ? saveButton : runButton).current?.click();
      });
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  });
  return <main>
    <header className="app-header"><a className="brand" href="#" onClick={event => event.preventDefault()}>iNode<span>Local workflows</span></a>
      <nav>{['Canvas', 'Runs', 'When', 'Plugins', 'Secrets'].map(name => <button key={name} className={screen === name ? 'active' : 'quiet'} onClick={() => {
        if (!valid()) return;
        setScreen(name);
        const listing = { Runs: [op.runs, setRuns], Plugins: [op.plugins, setPlugins] }[name];
        if (listing) perform(async () => { for await (const rows of call(listing[0])) listing[1](rows); });
      }}>{name}</button>)}</nav>
      <span className="local">● Local</span>
    </header>
    {error && <div className="notice problem" role="alert">{error}<button className="quiet" onClick={() => setError('')}>Close</button></div>}
    {screen === 'Canvas' ? <div className="workspace">
      <section className="work"><div className="toolbar">
        {runId ? <>
          <strong>{record?.node || 'Run'}</strong>
          <span className={'state ' + record?.state}>{record?.state}</span>
          <button onClick={() => { setRunId(''); setRecord(null); }}>Close run</button>
          {record?.workflow && !record.unsaved && <button disabled={pending.includes('open')} onClick={() => perform(() => open(record.node, record.id), 'open')}>Open workflow</button>}
          {record && record.state !== 'running' && record.state !== 'waiting' && <button disabled={busy} onClick={() => perform(async () => {
            for await (const id of call(op['runs again'], record.id)) showRun(id);
          })}>Run again</button>}
          {(record?.state === 'running' || record?.state === 'waiting') && <button onClick={() => perform(async () => {
            for await (const _ of call(op['runs stop'], record.id)) {}
          })}>Stop run</button>}
        </> : <>
          <button className="quiet" onClick={() => leave(() => replace({ name: '', version: '', workflow: empty }))}>New</button>
          <button title="Undo (Ctrl/Cmd+Z)" disabled={!edits.past.length} onClick={() => dispatch({ type: 'undo' })}>Undo</button>
          <button title="Redo (Shift+Ctrl/Cmd+Z)" disabled={!edits.future.length} onClick={() => dispatch({ type: 'redo' })}>Redo</button>
          {returnRun && <button disabled={busy || unsaved} title={unsaved ? 'Save changes first' : undefined} onClick={() => showRun(returnRun)}>Back to run</button>}
          <strong>{opened.name || 'New workflow'}</strong>
          <span className="save-state">{unsaved ? 'Unsaved' : opened.version ? 'Saved' : 'Not saved'}</span>
          <button ref={saveButton} title="Save (Ctrl/Cmd+S)" disabled={pending.includes('save')} onClick={save}>{pending.includes('save') ? 'Saving…' : 'Save'}</button>
          <button ref={runButton} title="Run (Ctrl/Cmd+Enter)" className="primary" disabled={pending.includes('run')} onClick={() => { if (valid()) setEnterInputs(true); }}>{pending.includes('run') ? 'Starting…' : 'Run'}</button>
        </>}
      </div>
        {!runId && <label className="description">Description
          <input aria-label="Workflow description" placeholder="What does this workflow do?" value={workflow.doc || ''} onFocus={() => { descriptionVisit.current = {}; }}
            onChange={event => { const value = event.target.value; setWorkflow(current => set(current, ['doc'], value), descriptionVisit.current); }} />
        </label>}
        {record?.error && <div className="notice problem">{record.error}</div>}
        <Source source={record?.source} showRun={showRun} fail={setError} />
        {runId && !record ? <p className="loading">Opening run…</p> :
          <Canvas key={record?.id || opened.key} workflow={record ? runWorkflow(record) : workflow} catalog={catalog}
            onEdit={runId ? undefined : setWorkflow} record={record} onFrom={record?.workflow && !busy ? from : undefined}
            onOpen={name => perform(() => open(name), 'open')} opening={pending.includes('open')} onCheck={setReport} />}
        {record && <details className="report" open><summary>Run outputs</summary><pre>{JSON.stringify(record.outputs, null, 2)}</pre></details>}
      </section>
    </div> : screen === 'Runs' ? <section className="runs">
      <h1>Runs</h1>{runs.length === 0 ? <p>No runs yet.</p> : <table>
        <thead><tr><th>Node</th><th>State</th><th>Created</th><th>Started by</th><th>Run</th></tr></thead>
        <tbody>{runs.map(run => <tr key={run.id}>
          <td><button className="quiet" onClick={() => showRun(run.id)}>{run.node}</button></td>
          <td><span className={'state ' + run.state}>{run.state}</span></td><td>{new Date(run.created || run.started).toLocaleString()}</td><td>{run.source ? <button className="quiet" onClick={() => showRun(run.source.run)}>{run.source.run} / {run.source.step}</button> : run.trigger || '—'}</td>
          <td><button className="quiet" onClick={() => showRun(run.id)}>{run.id}</button></td>
        </tr>)}</tbody>
      </table>}
    </section> : screen === 'When' ? <When catalog={catalog} perform={perform} busy={busy} fail={setError} showRun={showRun} /> : screen === 'Secrets' ? <Secrets perform={perform} busy={busy} fail={setError} /> : <section className="plugins">
      <h1>Plugins</h1>
      <form className="row" onSubmit={event => { event.preventDefault(); changePlugin(op.add, source); }}>
        <input aria-label="Plugin path or git URL" placeholder="Path or git URL" value={source} onChange={event => setSource(event.target.value)} required />
        <button disabled={busy} type="submit">Add plugin</button>
      </form>
      {manifest && <pre className="manifest" role="log" aria-label="Plugin manifest">{manifest}</pre>}
      {plugins.length === 0 ? <p>No plugins.</p> : <table>
        <thead><tr><th>Name</th><th>Version</th><th>State</th><th>What it says it accesses</th><th /></tr></thead>
        <tbody>{plugins.map(plugin => <tr key={plugin.name}>
          <td><strong>{plugin.name}</strong><small>{plugin.doc}</small></td><td>{plugin.version}</td><td>{plugin.state}</td>
          <td>{plugin.accesses?.length ? plugin.accesses.map((access, i) => <div key={i}>{access}</div>) : 'Not stated'}</td>
          <td><button disabled={busy} onClick={() => changePlugin(op.remove, plugin.name)}>Remove</button></td>
        </tr>)}</tbody>
      </table>}
    </section>}
    {preview && <Dialog className="box-editor" label="Run from here preview" onClose={() => setPreview(null)}>
        <div className="toolbar"><strong>Run from here</strong><button disabled={busy} onClick={() => from(preview.source.step, true)}>Start run</button><button disabled={busy} onClick={() => setPreview(null)}>Back</button></div>
        <Source source={preview.source} showRun={showRun} fail={setError} />
        {error && <p className="notice problem">{error}</p>}
        <Canvas workflow={preview.workflow} catalog={catalog} record={preview} />
    </Dialog>}
    {enterInputs && <Dialog className="run-inputs" label="Run inputs" onClose={() => setEnterInputs(false)}>
      <form onSubmit={event => { event.preventDefault(); run(); }}>
        <h2>Run inputs</h2>{Object.entries(workflow.inputs || {}).map(([name, port]) => <label className="port" key={name}>
          {name}<small>{type(port)}</small>
          <Field port={workflowDescription(report)?.inputs?.[name] || port} value={runInputs[name]} onChange={next => setRunInputs(set(runInputs, [name], next))} catalog={catalog} references={false} />
        </label>)}
        <div className="row"><button type="button" onClick={() => setEnterInputs(false)}>Cancel</button><button className="primary" type="submit">Run</button></div>
      </form>
    </Dialog>}
    {naming && <Dialog className="run-inputs" label="Save workflow" onClose={() => setNaming(false)}><form onSubmit={event => { event.preventDefault(); write(name); }}>
      <h2>Save workflow</h2><label>Name<input aria-label="Workflow name" autoFocus required value={name} onChange={event => setName(event.target.value)} /></label>
      <div className="row"><button type="button" onClick={() => setNaming(false)}>Cancel</button><button type="submit">Save</button></div>
    </form></Dialog>}
    {discard && <Dialog className="run-inputs" label="Unsaved changes" onClose={() => setDiscard(null)}>
      <h2>Unsaved changes</h2><p>This workflow has changes that have not been saved.</p><p>Keep editing to save them, or discard them to continue.</p>
      <div className="row"><button autoFocus onClick={() => setDiscard(null)}>Keep editing</button><button onClick={() => { const action = discard; setDiscard(null); action(); }}>Discard changes</button></div>
    </Dialog>}
  </main>;
}
