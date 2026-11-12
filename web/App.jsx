import { useEffect, useReducer, useRef, useState } from 'react';
import { Canvas, Dialog, Field, useReply } from './Canvas';
import { call, op } from './client';
import { content, emptyReport, history, inputValues, runMessages, runTrail, runWorkflow, set, type, workflowDescription } from './model';
import { When } from './When';
import { Secrets } from './Secrets';
import { Runs } from './Runs';
import { Duration, Value } from './Value';
import { Guide } from './Guide';

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
  const [runPath, setRunPath] = useState([]);
  const [screen, setScreen] = useState('Canvas');
  const [guide, setGuide] = useState(null);
  const [tutorial, setTutorial] = useState('');
  const [plugins, setPlugins] = useState([]);
  const [source, setSource] = useState('');
  const [manifest, setManifest] = useState('');
  const [runInputs, setRunInputs] = useState({});
  const [enterInputs, setEnterInputs] = useState(false);
  const [checked, setChecked] = useState({ document: null, report: emptyReport });
  const [runError, setRunError] = useState('');
  const [runRequest, setRunRequest] = useState(null);
  const [fromEditor, setFromEditor] = useState(false);
  const [pendingSource, setPendingSource] = useState(null);
  const [preview, setPreview] = useState(null);
  const [returnRun, setReturnRun] = useState('');
  const [error, setError] = useState('');
  const [pending, setPending] = useState([]);
  const [discard, setDiscard] = useState(null);
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState('');
  const latest = useRef({ workflow, opened }), saveButton = useRef(null), runButton = useRef(null), descriptionVisit = useRef({});
  latest.current = { workflow, opened, editing: screen === 'Canvas' && !runId };
  const busy = pending.includes('operation');
  const unsaved = JSON.stringify(workflow) !== JSON.stringify(opened.workflow);
  const savedDefinition = !!opened.version && content(workflow) === content(opened.workflow);
  const report = checked.report;
  const trail = record ? runTrail(record, runPath) : [];
  const shown = trail.at(-1)?.run;
  const values = inputValues(workflow, runInputs);
  const messages = runMessages(runError, Object.keys(workflow.inputs || {}));
  const runReason = checked.document !== content(workflow) ? '' : checked.error ? 'Fix the check error to run'
    : report.problems.length ? 'Fix problems to run' : '';
  const runDisabled = pending.includes('run') || pending.includes('inputs') || !!runReason;
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
    setRunId(''); setRecord(null); setScreen('Canvas'); setRunInputs({}); setRunError(''); setEnterInputs(false); setPendingSource(null); setReturnRun(back);
  };
  const open = async (name, back = '') => {
    for await (const node of call(op.show, name)) {
      leave(() => replace(node, back));
    }
  };
  useEffect(() => {
    perform(async () => {
      for (const [operation, receive] of [[op.help, setTutorial], [op.nodes, setCatalog], [op.runs, rows => setGuide(previous => previous ?? rows.length === 0)]]) {
        for await (const value of call(operation)) receive(value);
      }
    }, 'open');
  }, []);
  useReply(runId && screen === 'Canvas' && op['runs follow'], [runId], setRecord, setError);
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
    setRunError('');
    try {
      for await (const id of call(op.run, savedDefinition ? opened.name : workflow, values, opened.name || 'Unsaved workflow')) showRun(id, opened.key);
    } catch (error) {
      setRunError(error.message); setEnterInputs(true);
    }
  }, 'run');
  const requestInputs = () => {
    if (!Object.keys(workflow.inputs || {}).length) { run(); return; }
    if (enterInputs) { runButton.current?.focus(); return; }
    perform(async () => {
      const basedOn = opened;
      let previous;
      if (opened.version) for await (const rows of call(op.runs)) previous = rows.find(row => row.node === opened.name);
      if (latest.current.opened === basedOn) { setRunInputs(inputValues(latest.current.workflow, previous?.inputs || {})); setRunError(''); setEnterInputs(true); }
    }, 'inputs');
  };
  const requestRun = (choice = 'inputs') => { if (!runDisabled && valid()) setRunRequest({ choice, document: content(workflow) }); };
  useEffect(() => {
    if (!runRequest) return;
    if (screen !== 'Canvas' || runRequest.document !== content(workflow)) { setRunRequest(null); return; }
    if (checked.document !== runRequest.document) return;
    setRunRequest(null);
    if (!checked.error && !report.problems.length) {
      if (runRequest.choice === 'start') run(); else requestInputs();
    }
  }, [runRequest, checked, workflow, screen]);
  const changePlugin = (operation, source) => perform(async () => {
    setManifest('');
    try { for await (const text of call(operation, source)) setManifest(previous => previous + text); }
    finally {
      for (const [operation, receive] of [[op.plugins, setPlugins], [op.nodes, setCatalog]]) {
        for await (const rows of call(operation)) receive(rows);
      }
    }
  });
  const showRun = (id, editor = null) => { setPreview(null); setReturnRun(''); setPendingSource(null); setRunPath([]); setFromEditor(editor === latest.current.opened.key); setEnterInputs(false); setRecord(previous => previous?.id === id ? previous : null); setRunId(id); setScreen('Canvas'); };
  const backToEditing = () => {
    if (fromEditor || !record?.workflow) { setRunId(''); setRecord(null); }
    else if (!record.unsaved) perform(() => open(record.node, record.id), 'open');
    else leave(() => replace({ name: '', version: '', workflow: record.workflow }, record.id));
  };
  const useValue = path => { setPendingSource(path); setRunId(''); setRecord(null); };
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
      if (screen !== 'Canvas' || runId || naming || discard || preview) return;
      document.activeElement?.blur();
      requestAnimationFrame(() => {
        if (pressed === 'z') dispatch({ type: event.shiftKey ? 'redo' : 'undo' });
        else if (pressed === 'enter' && runButton.current?.form) runButton.current.form.requestSubmit();
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
        const listing = { Plugins: [op.plugins, setPlugins] }[name];
        if (listing) perform(async () => { for await (const rows of call(listing[0])) listing[1](rows); });
      }}>{name}</button>)}</nav>
      <span className="local">● Local</span><button className={guide ? 'active' : 'quiet'} aria-expanded={!!guide} onClick={() => setGuide(!guide)}>Guide</button>
    </header>
    {error && <div className="notice problem" role="alert">{error}<button className="quiet" onClick={() => setError('')}>Close</button></div>}
    <div className="page-content">{screen === 'Canvas' ? <div className="workspace">
      <section className="work"><div className={'toolbar ' + (runId ? 'run-bar' : '')}>
        {runId ? <>
          <div><small>Recorded run · Read-only</small>
            {trail.length ? <nav className="run-trail" aria-label="Run path">{trail.map((entry, i) => <span key={i}>
              {i > 0 && <span aria-hidden="true">›</span>}{i === trail.length - 1 ? <strong aria-current="page">{entry.label}</strong>
                : <button className="quiet" onClick={() => setRunPath(entry.path)}>{entry.label}</button>}
            </span>)}</nav> : <strong>Opening run…</strong>}
            {runPath.length > 0 && <strong>{shown.node}</strong>}
            {shown && <small>{shown.started ? 'Started ' + new Date(shown.started).toLocaleString() : 'Not started'}</small>}</div>
          <span className={'state ' + shown?.state}>{shown?.state}</span>
          {shown && <Duration record={shown} />}
          <button className="primary" disabled={pending.includes('open')} onClick={backToEditing}>Back to editing</button>
          {!runPath.length && <>
          {record && record.state !== 'running' && record.state !== 'waiting' && <button disabled={busy} onClick={() => perform(async () => {
            for await (const id of call(op['runs again'], record.id)) showRun(id, fromEditor ? opened.key : null);
          })}>Run again</button>}
          {(record?.state === 'running' || record?.state === 'waiting') && <button onClick={() => perform(async () => {
            for await (const _ of call(op['runs stop'], record.id)) {}
          })}>Stop run</button>}
          {record?.workflow && record.finished && <small>Select a step to inspect it or Run from here.</small>}
          </>}
        </> : <>
          <button className="quiet" onClick={() => leave(() => replace({ name: '', version: '', workflow: empty }))}>New</button>
          <button title="Undo (Ctrl/Cmd+Z)" disabled={!edits.past.length} onClick={() => dispatch({ type: 'undo' })}>Undo</button>
          <button title="Redo (Shift+Ctrl/Cmd+Z)" disabled={!edits.future.length} onClick={() => dispatch({ type: 'redo' })}>Redo</button>
          {returnRun && <button disabled={busy || unsaved} title={unsaved ? 'Save changes first' : undefined} onClick={() => showRun(returnRun, opened.key)}>Back to run</button>}
          <strong>{opened.name || 'New workflow'}</strong>
          <span className="save-state">{unsaved ? 'Unsaved' : opened.version ? 'Saved' : 'Not saved'}</span>
          <button ref={saveButton} title="Save (Ctrl/Cmd+S)" disabled={pending.includes('save')} onClick={save}>{pending.includes('save') ? 'Saving…' : 'Save'}</button>
          <button ref={enterInputs ? undefined : runButton} type="button" title={(checked.error || runReason || 'Run') + ' (Ctrl/Cmd+Enter)'} className="primary"
            disabled={runDisabled} onClick={() => requestRun()}>
            {pending.includes('run') ? 'Starting…' : pending.includes('inputs') ? 'Loading inputs…' : 'Run'}
          </button>
        </>}
      </div>
        {!runId && <label className="description">Description
          <input aria-label="Workflow description" placeholder="What does this workflow do?" value={workflow.doc || ''} onFocus={() => { descriptionVisit.current = {}; }}
            onChange={event => { const value = event.target.value; setWorkflow(current => set(current, ['doc'], value), descriptionVisit.current); }} />
        </label>}
        {!runId && enterInputs && <form id="run-inputs" className="run-panel" onSubmit={event => { event.preventDefault(); requestRun('start'); }}>
          <h2>Run inputs</h2>
          {messages.filter(message => !message.input).map((message, i) => <p className="problem" key={i}>{message.message}</p>)}
          <div className="run-fields">{Object.entries(workflow.inputs || {}).map(([name, declaration]) => {
            const port = workflowDescription(report)?.inputs?.[name] || declaration;
            const fallback = typeof declaration === 'object' ? declaration.default : undefined;
            const required = values[name] === undefined && fallback === undefined;
            return <div className="port" key={name}><label>{name}<small>{type(port)}</small></label>
              {required && <small>Required</small>}
              <Field port={port} value={values[name]} required={required} onChange={next => { setRunInputs(current => set(current, [name], next)); setRunError(''); }} catalog={catalog} references={false} />
              {fallback !== undefined && <div className="input-default"><small>Default</small><Value value={fallback} port={port} /></div>}
              {messages.filter(message => message.input === name).map((message, i) => <p className="problem" key={i}>{message.message}</p>)}
            </div>;
          })}</div>
          <div className="row"><button ref={runButton} type="submit" className="primary" disabled={runDisabled} title={(runReason || 'Start run') + ' (Ctrl/Cmd+Enter)'}>{pending.includes('run') ? 'Starting…' : 'Start run'}</button>
            <button type="button" onClick={() => setEnterInputs(false)}>Cancel</button></div>
        </form>}
        {shown?.error && <div className="notice problem">{shown.error}</div>}
        <Source source={shown?.source} showRun={showRun} fail={setError} />
        {runId && !record ? <p className="loading">Opening run…</p> :
          <Canvas key={runId ? runId + JSON.stringify(runPath) : opened.key} workflow={shown ? runWorkflow(shown) : workflow} catalog={catalog}
            onEdit={runId ? undefined : setWorkflow} record={shown} onFrom={!runPath.length && record?.workflow && record.finished && !busy ? from : undefined}
            onCall={record && (path => setRunPath([...runPath, ...path]))}
            onUse={!runPath.length && fromEditor && record?.finished ? useValue : undefined} pendingSource={!runId && pendingSource} onConnected={() => setPendingSource(null)}
            onOpen={runId ? undefined : name => perform(() => open(name), 'open')} opening={pending.includes('open')} onCheck={setChecked} />}
        {shown && <details className="report run-outputs" open><summary>Run outputs</summary><Value value={shown.outputs ?? undefined} port={{ fields: workflowDescription(report)?.outputs }} /></details>}
      </section>
    </div> : screen === 'Runs' ? <Runs showRun={showRun} fail={setError} /> : screen === 'When' ? <When catalog={catalog} perform={perform} busy={busy} fail={setError} showRun={showRun} /> : screen === 'Secrets' ? <Secrets perform={perform} busy={busy} fail={setError} /> : <section className="plugins">
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
    {guide && <Guide text={tutorial} onClose={() => setGuide(false)} />}</div>
    {preview && <Dialog className="box-editor" label="Run from here preview" onClose={() => setPreview(null)}>
        <div className="toolbar"><strong>Run from here</strong><button disabled={busy} onClick={() => from(preview.source.step, true)}>Start run</button><button disabled={busy} onClick={() => setPreview(null)}>Back</button></div>
        <Source source={preview.source} showRun={showRun} fail={setError} />
        {error && <p className="notice problem">{error}</p>}
        <Canvas workflow={preview.workflow} catalog={catalog} record={preview} />
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
