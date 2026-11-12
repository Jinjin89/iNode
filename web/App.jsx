import { useEffect, useState } from 'react';
import { Canvas, Field, useReply } from './Canvas';
import { call, op } from './client';
import { add, runWorkflow, set, type } from './model';

export function App() {
  const [catalog, setCatalog] = useState([]);
  const [workflow, setWorkflow] = useState({ inputs: {}, steps: {}, outputs: {} });
  const [opened, setOpened] = useState({ name: '', version: '' });
  const [record, setRecord] = useState(null);
  const [runId, setRunId] = useState('');
  const [screen, setScreen] = useState('Canvas');
  const [runs, setRuns] = useState([]);
  const [runInputs, setRunInputs] = useState({});
  const [enterInputs, setEnterInputs] = useState(false);
  const [search, setSearch] = useState('');
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(true);
  const perform = async action => {
    setBusy(true); setError('');
    try { await action(); } catch (error) { setError(error.message); }
    finally { setBusy(false); }
  };
  const edit = next => { setWorkflow(next); setMessage('Unsaved'); };
  const open = async name => {
    for await (const node of call(op.show, name)) {
      setWorkflow(node.workflow); setOpened({ name, version: node.version });
      setRunId(''); setRecord(null); setScreen('Canvas'); setRunInputs({}); setMessage('');
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
      setOpened({ name: node.name, version: node.version }); setWorkflow(node.workflow); setMessage('Saved');
    }
    for await (const nodes of call(op.nodes)) setCatalog(nodes);
  });
  const run = () => perform(async () => {
    setEnterInputs(false);
    for await (const id of call(op.run, workflow, runInputs, opened.name || 'Unsaved workflow')) showRun(id);
  });
  const showRun = id => { setRecord(previous => previous?.id === id ? previous : null); setRunId(id); setScreen('Canvas'); };
  return <main>
    <header className="app-header"><a className="brand" href="#" onClick={event => event.preventDefault()}>iNode<span>Local workflows</span></a>
      <nav>{['Canvas', 'Runs'].map(name => <button key={name} className={screen === name ? 'active' : 'quiet'} onClick={() => {
        setScreen(name);
        if (name === 'Runs') perform(async () => { for await (const rows of call(op.runs)) setRuns(rows); });
      }}>{name}</button>)}</nav>
      <span className="local">● Local</span>
    </header>
    {error && <div className="notice problem" role="alert">{error}<button className="quiet" onClick={() => setError('')}>Close</button></div>}
    {screen === 'Canvas' ? <div className="workspace">
      <aside className="catalog"><h2>Nodes</h2><input aria-label="Find a node" placeholder="Find a node…" value={search} onChange={event => setSearch(event.target.value)} />
        <p>Choose a node to add a step.</p>
        <div className="node-list">{catalog.filter(node => (node.name + ' ' + node.doc).toLowerCase().includes(search.toLowerCase())).map(node =>
          <button disabled={!!runId || busy} key={node.name} onClick={() => edit(add(workflow, node.name))}>
            <strong>{node.name}</strong><small>{node.doc}</small>
          </button>)}
        </div>
      </aside>
      <section className="work"><div className="toolbar">
        {runId ? <>
          <strong>{record?.node || 'Run'}</strong>
          <span className={'state ' + record?.state}>{record?.state}</span>
          <button onClick={() => { setRunId(''); setRecord(null); }}>Close run</button>
          {(record?.state === 'running' || record?.state === 'waiting') && <button onClick={() => perform(async () => {
            for await (const _ of call(op['runs stop'], record.id)) {}
          })}>Stop run</button>}
        </> : <>
          <select aria-label="Open workflow" disabled={busy} value=""
            onChange={event => { if (event.target.value) perform(() => open(event.target.value)); }}>
            <option value="">Open workflow</option>{catalog.filter(node => node.workflow).map(node => <option key={node.name}>{node.name}</option>)}
          </select>
          <button className="quiet" disabled={busy} onClick={() => { edit({ inputs: {}, steps: {}, outputs: {} }); setOpened({ name: '', version: '' }); setRunInputs({}); }}>New</button>
          <input aria-label="Workflow name" readOnly={!!opened.version || busy} placeholder="Workflow name" value={opened.name} onChange={event => setOpened({ name: event.target.value, version: '' })} />
          <span className="save-state">{busy ? 'Working…' : message}</span><button disabled={busy} onClick={save}>Save</button><button className="primary" disabled={busy} onClick={() => setEnterInputs(true)}>Run</button>
        </>}
      </div>
        {!runId && <label className="description">Description
          <input aria-label="Workflow description" disabled={busy} placeholder="What does this workflow do?" value={workflow.doc || ''}
            onChange={event => edit(set(workflow, ['doc'], event.target.value))} />
        </label>}
        {record?.error && <div className="notice problem">{record.error}</div>}
        {runId && !record ? <p className="loading">Opening run…</p> :
          <Canvas key={record?.id || (opened.version ? opened.name : 'new')} workflow={record ? runWorkflow(record) : workflow} catalog={catalog}
            onEdit={runId || busy ? undefined : edit} record={record} />}
        {record && <details className="report" open><summary>Run outputs</summary><pre>{JSON.stringify(record.outputs, null, 2)}</pre></details>}
      </section>
    </div> : <section className="runs">
      <h1>Runs</h1>{runs.length === 0 ? <p>No runs yet.</p> : <table>
        <thead><tr><th>Node</th><th>State</th><th>Started</th><th>Run</th></tr></thead>
        <tbody>{runs.map(run => <tr key={run.id}>
          <td><button className="quiet" onClick={() => showRun(run.id)}>{run.node}</button></td>
          <td><span className={'state ' + run.state}>{run.state}</span></td><td>{new Date(run.started).toLocaleString()}</td>
          <td><button className="quiet" onClick={() => showRun(run.id)}>{run.id}</button></td>
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
