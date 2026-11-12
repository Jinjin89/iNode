import { useEffect, useState } from 'react';
import { Canvas, Field, NodeList, useReply } from './Canvas';
import { call, op } from './client';
import { content, runWorkflow, set, type } from './model';

export function App() {
  const [catalog, setCatalog] = useState([]);
  const [workflow, setWorkflow] = useState({ inputs: {}, steps: {}, outputs: {} });
  const [opened, setOpened] = useState({ name: '', version: '', workflow: null });
  const [record, setRecord] = useState(null);
  const [runId, setRunId] = useState('');
  const [screen, setScreen] = useState('Canvas');
  const [runs, setRuns] = useState([]);
  const [plugins, setPlugins] = useState([]);
  const [source, setSource] = useState('');
  const [manifest, setManifest] = useState('');
  const [runInputs, setRunInputs] = useState({});
  const [enterInputs, setEnterInputs] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(true);
  const unsaved = JSON.stringify(workflow) !== JSON.stringify(opened.workflow);
  const savedDefinition = !!opened.version && content(workflow) === content(opened.workflow);
  const perform = async action => {
    setBusy(true); setError('');
    try { await action(); } catch (error) { setError(error.message); }
    finally { setBusy(false); }
  };
  const open = async name => {
    for await (const node of call(op.show, name)) {
      setWorkflow(node.workflow); setOpened({ name, version: node.version, workflow: node.workflow });
      setRunId(''); setRecord(null); setScreen('Canvas'); setRunInputs({});
    }
  };
  useEffect(() => {
    perform(async () => {
      for await (const nodes of call(op.nodes)) {
        setCatalog(nodes);
        const first = nodes.find(node => node.workflow);
        if (first) await open(first.name);
      }
    });
  }, []);
  useReply(runId && op['runs follow'], [runId], setRecord, setError);
  const save = () => perform(async () => {
    for await (const node of call(op.save, opened.name, opened.version, workflow)) {
      setOpened({ name: node.name, version: node.version, workflow: node.workflow }); setWorkflow(node.workflow);
    }
    for await (const nodes of call(op.nodes)) setCatalog(nodes);
  });
  const run = () => perform(async () => {
    setEnterInputs(false);
    for await (const id of call(op.run, savedDefinition ? opened.name : workflow, runInputs, opened.name || 'Unsaved workflow')) showRun(id);
  });
  const changePlugin = (operation, source) => perform(async () => {
    setManifest('');
    try { for await (const text of call(operation, source)) setManifest(previous => previous + text); }
    finally {
      for (const [operation, receive] of [[op.plugins, setPlugins], [op.nodes, setCatalog]]) {
        for await (const rows of call(operation)) receive(rows);
      }
    }
  });
  const showRun = id => { setRecord(previous => previous?.id === id ? previous : null); setRunId(id); setScreen('Canvas'); };
  return <main>
    <header className="app-header"><a className="brand" href="#" onClick={event => event.preventDefault()}>iNode<span>Local workflows</span></a>
      <nav>{['Canvas', 'Runs', 'Plugins'].map(name => <button key={name} className={screen === name ? 'active' : 'quiet'} onClick={() => {
        setScreen(name);
        const listing = { Runs: [op.runs, setRuns], Plugins: [op.plugins, setPlugins] }[name];
        if (listing) perform(async () => { for await (const rows of call(listing[0])) listing[1](rows); });
      }}>{name}</button>)}</nav>
      <span className="local">● Local</span>
    </header>
    {error && <div className="notice problem" role="alert">{error}<button className="quiet" onClick={() => setError('')}>Close</button></div>}
    {screen === 'Canvas' ? <div className="workspace">
      <NodeList catalog={catalog} workflow={workflow} onEdit={runId || busy ? undefined : setWorkflow} />
      <section className="work"><div className="toolbar">
        {runId ? <>
          <strong>{record?.node || 'Run'}</strong>
          <span className={'state ' + record?.state}>{record?.state}</span>
          <button onClick={() => { setRunId(''); setRecord(null); }}>Close run</button>
          {record && record.state !== 'running' && record.state !== 'waiting' && <button disabled={busy} onClick={() => perform(async () => {
            for await (const id of call(op['runs again'], record.id)) showRun(id);
          })}>Run again</button>}
          {(record?.state === 'running' || record?.state === 'waiting') && <button onClick={() => perform(async () => {
            for await (const _ of call(op['runs stop'], record.id)) {}
          })}>Stop run</button>}
        </> : <>
          <select aria-label="Open workflow" disabled={busy} value=""
            onChange={event => { if (event.target.value) perform(() => open(event.target.value)); }}>
            <option value="">Open workflow</option>{catalog.filter(node => node.workflow).map(node => <option key={node.name}>{node.name}</option>)}
          </select>
          <button className="quiet" disabled={busy} onClick={() => { setWorkflow({ inputs: {}, steps: {}, outputs: {} }); setOpened({ name: '', version: '', workflow: null }); setRunInputs({}); }}>New</button>
          <input aria-label="Workflow name" readOnly={!!opened.version || busy} placeholder="Workflow name" value={opened.name} onChange={event => setOpened({ ...opened, name: event.target.value })} />
          <span className="save-state">{busy ? 'Working…' : unsaved ? 'Unsaved' : 'Saved'}</span><button disabled={busy} onClick={save}>Save</button><button className="primary" disabled={busy} onClick={() => setEnterInputs(true)}>Run</button>
        </>}
      </div>
        {!runId && <label className="description">Description
          <input aria-label="Workflow description" disabled={busy} placeholder="What does this workflow do?" value={workflow.doc || ''}
            onChange={event => setWorkflow(set(workflow, ['doc'], event.target.value))} />
        </label>}
        {record?.error && <div className="notice problem">{record.error}</div>}
        {runId && !record ? <p className="loading">Opening run…</p> :
          <Canvas key={record?.id || (opened.version ? opened.name : 'new')} workflow={record ? runWorkflow(record) : workflow} catalog={catalog}
            onEdit={runId || busy ? undefined : setWorkflow} record={record} />}
        {record && <details className="report" open><summary>Run outputs</summary><pre>{JSON.stringify(record.outputs, null, 2)}</pre></details>}
      </section>
    </div> : screen === 'Runs' ? <section className="runs">
      <h1>Runs</h1>{runs.length === 0 ? <p>No runs yet.</p> : <table>
        <thead><tr><th>Node</th><th>State</th><th>Started</th><th>Run</th></tr></thead>
        <tbody>{runs.map(run => <tr key={run.id}>
          <td><button className="quiet" onClick={() => showRun(run.id)}>{run.node}</button></td>
          <td><span className={'state ' + run.state}>{run.state}</span></td><td>{new Date(run.started).toLocaleString()}</td>
          <td><button className="quiet" onClick={() => showRun(run.id)}>{run.id}</button></td>
        </tr>)}</tbody>
      </table>}
    </section> : <section className="plugins">
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
    {enterInputs && <div className="modal-backdrop">
      <form className="run-inputs" onSubmit={event => { event.preventDefault(); run(); }}>
        <h2>Run inputs</h2>{Object.entries(workflow.inputs || {}).map(([name, port]) => <label className="port" key={name}>
          {name}<small>{type(port)}</small>
          <Field port={port} value={runInputs[name]} onChange={next => setRunInputs(set(runInputs, [name], next))} catalog={catalog} />
        </label>)}
        <div className="row"><button type="button" onClick={() => setEnterInputs(false)}>Cancel</button><button className="primary" type="submit">Run</button></div>
      </form>
    </div>}
  </main>;
}
