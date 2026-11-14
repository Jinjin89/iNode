import { useState } from 'react';
import { useReply } from './Canvas';
import { call, op } from './client';
import { commandText } from './model';

function Details({ plugin }) {
  return <><strong>{plugin.name} · {plugin.version}</strong><small>Source: {plugin.source}</small>{plugin.doc && <p>{plugin.doc}</p>}<h3>What it says it accesses</h3>
    {plugin.accesses?.length ? <ul>{plugin.accesses.map((text, i) => <li key={i}>{text}</li>)}</ul> : <p>No access description available.</p>}
    {plugin.process && <><small>Program: {commandText(plugin.process)}</small>{Object.entries(plugin.env || {}).map(([key, name]) => <small key={key}>{key} from secret {name}</small>)}</>}</>;
}

export function Plugins({ catalog, setCatalog, perform, pending, ask, fail, log, setLog, changed, setChanged }) {
  const [rows, setRows] = useState([]), [source, setSource] = useState('');
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
    for await (const plugin of call(op['plugins inspect'], source)) ask('Install plugin?', <><Details plugin={plugin} /><p>Install this plugin in this project? Replacing it stops its running process and calls.</p></>, 'Install plugin', () => change(op.add, plugin.source, plugin));
  }, 'plugin inspect');
  return <section className="plugins">
    <h1>Plugins</h1><p>Plugins add nodes to your project. Enter a folder or git URL to see what it says it accesses before installing.</p>
    <form className="screen-form" onSubmit={event => { event.preventDefault(); inspectSource(source); }}>
      <header className="form-heading"><h2>Add a plugin</h2><button className="primary" disabled={pending.includes('plugin inspect') || adding} type="submit">{pending.includes('plugin inspect') ? 'Inspecting…' : adding ? 'Installing…' : 'Add plugin'}</button></header><div className="row"><label>Path or git URL<input aria-label="Plugin path or git URL" value={source} onChange={event => setSource(event.target.value)} required /></label>
      </div>
    </form>
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
