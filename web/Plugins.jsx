import { useCallback, useState } from 'react';
import { Dialog, Field, useReply } from './Canvas';
import { call, op } from './client';
import { commandText } from './model';

function Details({ plugin }) {
  return <><strong>{plugin.name} · {plugin.version}</strong>{plugin.source && <small>Source: {plugin.source}</small>}{plugin.doc && <p>{plugin.doc}</p>}<h3>What it says it accesses</h3>
    {plugin.accesses?.length ? <ul>{plugin.accesses.map((text, i) => <li key={i}>{text}</li>)}</ul> : <p>No access description available.</p>}
    {plugin.process && <><small>Program: {commandText(plugin.process)}</small>{Object.entries(plugin.env || {}).map(([key, name]) => <small key={key}>{key} from secret {name}</small>)}</>}</>;
}

function Inspection({ draft, setDraft, change, pending, showSecrets }) {
  const [checked, setChecked] = useState(null), [error, setError] = useState('');
  const receive = useCallback(plugin => setChecked({ draft, plugin }), [draft]);
  useReply(op['plugins inspect'], [draft], receive, setError);
  const plugin = checked?.plugin, prepared = plugin?.preparation, ready = checked?.draft === draft && !error && !plugin?.error;
  const edit = (name, value) => setDraft({ ...draft, [name]: value });
  return <Dialog className="run-inputs screen-form" label="Install plugin?" onClose={() => setDraft(null)}><form onSubmit={event => { event.preventDefault(); if (ready && plugin?.name) { change(op.add, draft, plugin); setDraft(null); } }}>
    <h2>Install plugin?</h2>{prepared?.message && <p>{prepared.message}</p>}
    {prepared?.editable?.map(field => {
      const value = draft[field.name] ?? field.value;
      return <section className="port" key={field.name}><label>{field.label}</label>{field.kind === 'choice' ? <select aria-label={field.label} required value={value ?? ''} onChange={event => edit(field.name, event.target.value)}>
        <option value="" disabled>Choose</option>{Object.entries(field.choices).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
      </select> : field.kind === 'bindings' ? <>
        {Object.entries(value || {}).map(([key, secret], index, entries) => {
          const update = (name, secret) => edit(field.name, Object.fromEntries(entries.map((entry, i) => i === index ? [name, secret] : entry)));
          return <div className="row" key={index}><label>Variable name<input required aria-label="Variable name" value={key} onChange={event => { const name = event.target.value; event.target.setCustomValidity(name !== key && Object.hasOwn(value, name) ? 'Choose a distinct variable name' : ''); if (event.target.validity.valid) update(name, secret); }} /></label>
            <label>Stored secret<Field port={{ kind: 'secret', type: 'secret' }} value={secret} required references={false} onChange={next => update(key, next ?? '')} /></label>
            <button type="button" onClick={() => edit(field.name, Object.fromEntries(entries.filter((_, i) => i !== index)))}>Remove</button></div>;
        })}
        <div className="row"><button type="button" disabled={Object.hasOwn(value || {}, '')} onClick={() => edit(field.name, { ...value, '': '' })}>Add variable</button><button type="button" className="quiet" onClick={showSecrets}>Manage secrets</button></div>
      </> : <input aria-label={field.label} required value={value ?? ''} onChange={event => edit(field.name, event.target.value)} />}</section>;
    })}
    {plugin?.name && <Details plugin={{ ...plugin, source: '' }} />}{prepared?.working_dir && <p>Working folder: {prepared.working_dir}</p>}
    {prepared?.manifest && <details><summary>Manifest</summary><pre className="manifest">{JSON.stringify(prepared.manifest, null, 2)}</pre></details>}
    {!ready && !error && !plugin?.error && <p>Inspecting…</p>}{(error || plugin?.error) && <p className="problem" role="alert">{error || plugin.error}</p>}
    <p>Replacing a plugin stops its running process and calls.</p><div className="row"><button type="button" onClick={() => setDraft(null)}>Cancel</button><button type="submit" className="primary" disabled={!ready || !plugin?.name || pending.some(name => name.startsWith('plugin add:'))}>Install plugin</button></div>
  </form></Dialog>;
}

export function Plugins({ catalog, setCatalog, perform, pending, ask, fail, log, setLog, changed, setChanged, draft, setDraft, showSecrets }) {
  const [rows, setRows] = useState([]), [source, setSource] = useState(''), [sourceError, setSourceError] = useState('');
  useReply(op.plugins, [], setRows, fail, 1000);
  const change = (operation, value, confirmed) => perform(async () => {
    try { for await (const text of call(operation, value)) setLog(previous => previous + text); }
    finally {
      for await (const rows of call(op.plugins)) {
        setRows(rows);
        if (confirmed) {
          const installed = [...rows].reverse().find(row => row.source === confirmed.source && row.state !== 'missing');
          if (installed) {
            const fields = ['name', 'version', 'accesses', 'process', 'env'].filter(key => JSON.stringify(installed[key] ?? []) !== JSON.stringify(confirmed[key] ?? []));
            setChanged(current => ({ ...current, [installed.name]: fields.join(', ') }));
          }
        }
      }
      for await (const nodes of call(op.nodes)) setCatalog(nodes);
    }
  }, confirmed ? 'plugin add:' + confirmed.name : 'plugin:' + value);
  const adding = pending.some(name => name.startsWith('plugin add:'));
  const inspectSource = source => perform(async () => {
    setSourceError('');
    try { for await (const plugin of call(op['plugins inspect'], source)) {
      if (plugin.preparation?.editable?.length) setDraft(plugin.preparation.input);
      else ask('Install plugin?', <><Details plugin={plugin} /><p>Install this plugin in this project? Replacing it stops its running process and calls.</p></>, 'Install plugin', () => change(op.add, plugin.source, plugin));
    } } catch (error) { setSourceError(error.message); }
  }, 'plugin inspect');
  return <section className="plugins">
    <h1>Plugins</h1><p>Plugins add nodes to your project. Enter a folder, a git URL or an MCP server command to see what it will do before installing.</p>
    <form className="screen-form" onSubmit={event => { event.preventDefault(); inspectSource(source); }}>
      <header className="form-heading"><h2>Add a plugin</h2><button className="primary" disabled={pending.includes('plugin inspect') || adding} type="submit">{pending.includes('plugin inspect') ? 'Inspecting…' : adding ? 'Installing…' : 'Add plugin'}</button></header><div className="row"><label>Plugin source<input aria-label="Plugin source" value={source} onChange={event => setSource(event.target.value)} required /></label>
      </div>
    </form>
    {sourceError && <p className="problem">{sourceError}</p>}{draft && <Inspection draft={draft} setDraft={setDraft} change={change} pending={pending} showSecrets={showSecrets} />}
    {log && <section className="install-log"><div className="row"><h2>Install log</h2><button onClick={() => setLog('')}>Dismiss log</button></div><pre className="manifest" role="log" aria-label="Plugin install log">{log}</pre></section>}
    {!rows.length ? <p>Add your first plugin above to make its nodes available on Canvas.</p> : <div className="table-scroll"><table>
      <thead><tr><th>Plugin</th><th>State</th><th>Nodes</th><th>Actions</th></tr></thead>
      <tbody>{rows.map(plugin => {
        const working = pending.includes('plugin:' + plugin.name) || pending.includes('plugin add:' + plugin.name);
        return <tr key={plugin.name} aria-busy={working}>
          <td><Details plugin={plugin} />{changed[plugin.name] && <p className="problem">Changed since confirmation: {changed[plugin.name]}. Review the installed details above.</p>}</td>
          <td>{plugin.state}{plugin.error && <p className="problem">{plugin.error}</p>}</td><td>{catalog.filter(node => node.name.startsWith(plugin.name + '.')).map(node => <div key={node.name}>{node.name}</div>)}</td>
          <td><button disabled={working || adding} onClick={() => inspectSource(plugin.source)}>Add again</button><button disabled={working} onClick={() => ask('Remove plugin?', 'Remove plugin “' + plugin.name + '” and its installed files? Workflows using its nodes will report it missing.', 'Remove plugin', () => change(op.remove, plugin.name))}>{working ? 'Working…' : 'Remove'}</button></td>
        </tr>;
      })}</tbody>
    </table></div>}
  </section>;
}
