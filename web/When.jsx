import { useEffect, useState } from 'react';
import { Field, Messages, useReply } from './Canvas';
import { call, op } from './client';
import { emptyReport, inputs, problems, set, type } from './model';

export function When({ catalog, perform, busy, fail, showRun }) {
  const [rows, setRows] = useState([]);
  const [draft, edit] = useState({ inputs: {}, steps: { event: { use: '', in: {} }, run: { use: '', in: {}, after: ['event'] } }, outputs: {} });
  const [report, setReport] = useState(emptyReport);
  useReply(op.check, [draft], setReport, fail);
  useEffect(() => {
    let current = true, timer;
    const read = async () => {
      try { for await (const rows of call(op['trigger list'])) if (current) setRows(rows); }
      catch (error) { if (current) fail(error.message); }
      if (current) timer = setTimeout(read, 1000);
    };
    read();
    return () => { current = false; clearTimeout(timer); };
  }, [fail]);
  const change = (operation, ...args) => perform(async () => {
    for await (const _ of call(operation, ...args)) {}
    for await (const rows of call(op['trigger list'])) setRows(rows);
  });
  const event = catalog.find(node => node.name === draft.steps.event.use);
  const runLink = run => run ? <button className="quiet" title={run.id} onClick={() => showRun(run.id)}>
    <span className={'state ' + run.state}>{run.state}</span><small>{new Date(run.finished || run.started || run.created).toLocaleString()}</small>
  </button> : '—';
  const instruction = flow => {
    const event = flow?.steps?.event, run = flow?.steps?.run;
    return event && run && [event.use, ...Object.entries(event.in || {}).map(([name, value]) => name + '=' + JSON.stringify(value)),
      '->', typeof run.use === 'string' ? run.use : 'Inline workflow'].join(' ');
  };
  return <section className="when">
    <h1>When</h1>
    {rows.length === 0 ? <p>No triggers.</p> : <table>
      <thead><tr><th>Trigger</th><th>On</th><th>Current run</th><th>Waiting events</th><th>Last run</th><th>Last failure</th><th /></tr></thead>
      <tbody>{rows.map(row => <tr key={row.name}>
        <td><strong>{row.name}</strong><small>{instruction(row.workflow)}</small>{row.error && <p className="problem">{row.error}</p>}</td>
        <td><input aria-label={'On ' + row.name} type="checkbox" checked={row.state === 'on'} disabled={busy}
          onChange={event => change(event.target.checked ? op['trigger on'] : op['trigger off'], row.name)} /></td>
        <td>{runLink(row.current)}</td><td>{row.waiting}</td><td>{runLink(row.last)}</td><td>{runLink(row.last_failure)}</td>
        <td><div className="row"><button disabled={busy} onClick={() => change(op['trigger clear'], row.name)}>Clear waiting</button>
          <button disabled={busy} onClick={() => change(op['trigger remove'], row.name)}>Remove</button></div></td>
      </tr>)}</tbody>
    </table>}
    <form className="trigger-form" onSubmit={event => { event.preventDefault(); change(op['trigger add'], draft); }}>
      <h2>Add trigger</h2><div className="trigger-steps">
        {['event', 'run'].map(key => {
          const step = draft.steps[key];
          const node = catalog.find(node => node.name === step.use);
          const ports = { ...inputs(step.use, step.in, catalog), ...report.nodes[JSON.stringify(['steps', key])]?.inputs };
          return <section key={key}><h3>{key === 'event' ? 'When' : 'Run'}</h3>
            <select aria-label={key === 'event' ? 'Trigger kind' : 'Node to run'} value={step.use}
              onChange={event => edit(set(draft, ['steps', key], { ...step, use: event.target.value, in: {} }))}>
              <option value="">Choose a {key === 'event' ? 'trigger kind' : 'node'}</option>
              {catalog.filter(node => key !== 'event' || node.trigger).map(node => <option key={node.name}>{node.name}</option>)}
            </select><p>{node?.doc}</p>
            <Messages items={problems(report, '(inline)/' + key, 'use')} />
            {Object.entries(ports).map(([name, port]) => <div className="port" key={name}>
              <label>{name}<small>{type(port)}</small></label>
              <Field port={port} value={step.in[name]} catalog={catalog} onChange={next => edit(set(draft, ['steps', key, 'in', name], next))} />
              {key === 'run' && <select aria-label={'Event output for ' + name} value="" onChange={event => edit(set(draft, ['steps', key, 'in', name], '=event.' + event.target.value))}>
                <option value="">Use event output</option>{Object.keys(event?.outputs || {}).map(name => <option key={name}>{name}</option>)}
              </select>}
              <Messages items={problems(report, '(inline)/' + key, name)} />
            </div>)}
          </section>;
        })}
      </div><button disabled={busy} type="submit">Add trigger</button>
    </form>
  </section>;
}
