import { useState } from 'react';
import { useReply } from './Canvas';
import { op } from './client';
import { filterRuns } from './model';
import { Duration } from './Value';

export function Runs({ showRun, fail }) {
  const [runs, setRuns] = useState([]), [node, setNode] = useState(''), [state, setState] = useState('');
  useReply(op.runs, [], setRuns, fail, 1000);
  const rows = filterRuns(runs, node, state);
  return <section className="runs"><h1>Runs</h1>
    <p>A run records what each step received and returned. Open a workflow on the Canvas and press Run to start one.</p>
    <div className="run-filters row"><label>Workflow<select aria-label="Filter by workflow" value={node} onChange={event => setNode(event.target.value)}>
      <option value="">All workflows and nodes</option>{[...new Set([node, ...runs.map(run => run.node)])].filter(Boolean).sort().map(name => <option key={name}>{name}</option>)}
    </select></label><label>State<select aria-label="Filter by state" value={state} onChange={event => setState(event.target.value)}>
      <option value="">All states</option>{[...new Set([state, ...runs.map(run => run.state)])].filter(Boolean).sort().map(name => <option key={name}>{name}</option>)}
    </select></label></div>
    {!rows.length ? <p>{runs.length ? 'No runs match these filters.' : 'No runs yet.'}</p> : <table>
      <thead><tr><th>Node</th><th>State</th><th>Created</th><th>Duration</th><th>Started by</th><th>Run</th></tr></thead>
      <tbody>{rows.map(run => <tr key={run.id}>
        <td><button className="quiet" onClick={() => showRun(run.id)}>{run.node}</button>{run.unsaved && <small>Unsaved</small>}</td>
        <td><span className={'state ' + run.state}>{run.state}</span></td><td>{new Date(run.created || run.started).toLocaleString()}</td><td><Duration record={run} /></td>
        <td>{run.source ? <button className="quiet" onClick={() => showRun(run.source.run)}>{run.source.run} / {run.source.step}</button> : run.trigger || 'Manual'}</td>
        <td><button className="quiet" onClick={() => showRun(run.id)}>{run.id}</button></td>
      </tr>)}</tbody>
    </table>}
  </section>;
}
