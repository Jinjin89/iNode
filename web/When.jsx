import { useCallback, useEffect, useState } from 'react';
import { Field, Messages, useReply } from './Canvas';
import { call, op } from './client';
import { content, emptyReport, inputs, problems, set, type } from './model';

export function When({ catalog, perform, pending, ask, fail, showRun, showRuns }) {
  const [rows, setRows] = useState([]);
  const [draft, edit] = useState({ inputs: {}, steps: { event: { use: '', in: {} }, run: { use: '', in: {}, after: ['event'] } }, outputs: {} });
  const [checked, setChecked] = useState({ document: '', report: emptyReport });
  const [submitted, submit] = useState(null);
  const key = content(draft), report = checked.report;
  const fresh = checked.document === key;
  const receive = useCallback(report => setChecked({ document: key, report }), [key]);
  useReply(op.check, [draft], receive, fail);
  useReply(op['trigger list'], [], setRows, fail, 1000);
  const change = (operation, name) => perform(async () => {
    for await (const _ of call(operation, name)) {}
    for await (const rows of call(op['trigger list'])) setRows(rows);
  }, operation === op['trigger add'] ? 'trigger add' : 'trigger:' + name);
  useEffect(() => {
    if (!submitted) return;
    if (submitted !== key) { submit(null); return; }
    if (checked.document !== submitted) return;
    submit(null);
    if (!report.problems.length) change(op['trigger add'], draft);
  }, [submitted, key, checked]);
  const event = catalog.find(node => node.name === draft.steps.event.use);
  const runLink = run => run ? <button className="quiet" title={run.id} onClick={() => showRun(run.id)}>
    <span className={'state ' + run.state}>{run.state}</span><small>{new Date(run.finished || run.started || run.created).toLocaleString()}</small>
  </button> : '—';
  const instruction = step => <>{typeof step.use === 'string' ? step.use : 'Inline workflow'}{Object.keys(step.in || {}).length > 0 && <> ({Object.entries(step.in).map(([name, value]) => name + ' ' + (typeof value === 'string' ? value : JSON.stringify(value))).join(', ')})</>}</>;
  const choosing = !draft.steps.event.use || !draft.steps.run.use;
  const reason = choosing ? 'Choose when and what to run' : fresh && report.problems.length ? 'Fix the marked inputs' : '';
  return <section className="when">
    <h1>When</h1><p>Triggers start runs when something happens. Add one below, then switch it on.</p>
    {rows.length > 0 && <div className="table-scroll"><table>
      <thead><tr><th>Trigger</th><th>On/off</th><th>Current run</th><th>Waiting</th><th>Last run</th><th>Last failure</th><th>Actions</th></tr></thead>
      <tbody>{rows.map(row => {
        const working = pending.includes('trigger:' + row.name);
        return <tr key={row.name} aria-busy={working}>
          <td><strong>{row.name}</strong><p className="trigger-sentence">{row.workflow?.steps?.event && row.workflow?.steps?.run && <>When {instruction(row.workflow.steps.event)} run {instruction(row.workflow.steps.run)}</>}</p>
            <button className="quiet" onClick={() => showRuns(row.name)}>View runs</button>{row.error && <p className="problem">{row.error}</p>}</td>
          <td><label className="row"><input aria-label={'On ' + row.name} type="checkbox" checked={row.state === 'on'} disabled={working}
            onChange={event => change(event.target.checked ? op['trigger on'] : op['trigger off'], row.name)} />{row.state}</label>{working && <small role="status">Working…</small>}</td>
          <td>{runLink(row.current)}</td><td>{row.waiting}</td><td>{runLink(row.last)}</td><td>{runLink(row.last_failure)}</td>
          <td><div className="actions"><button disabled={working} onClick={() => ask('Clear waiting events?', 'Delete all waiting events for trigger “' + row.name + '”? Its current run will continue.', 'Clear waiting', () => change(op['trigger clear'], row.name))}>Clear waiting</button>
            <button disabled={working} onClick={() => ask('Remove trigger?', 'Remove trigger “' + row.name + '” and its waiting events? Its current run will continue.', 'Remove trigger', () => change(op['trigger remove'], row.name))}>Remove</button></div></td>
        </tr>;
      })}</tbody>
    </table></div>}
    <form className="screen-form" onSubmit={event => { event.preventDefault(); submit(key); }}>
      <header className="form-heading"><h2>Add trigger</h2><button className="primary" disabled={pending.includes('trigger add') || !!reason} title={reason || 'Add trigger'} type="submit">{pending.includes('trigger add') ? 'Adding…' : 'Add trigger'}</button></header><div className="trigger-steps">
        {['event', 'run'].map(key => {
          const step = draft.steps[key];
          const node = catalog.find(node => node.name === step.use);
          const ports = { ...inputs(step.use, step.in, catalog), ...report.nodes[JSON.stringify(['steps', key])]?.inputs };
          return <section key={key}><h3>{key === 'event' ? 'When' : 'Run'}</h3>
            <select aria-label={key === 'event' ? 'Trigger kind' : 'Node to run'} value={step.use}
              onChange={event => edit(set(draft, ['steps', key], { ...step, use: event.target.value, in: {} }))}>
              <option value="">Choose a {key === 'event' ? 'trigger kind' : 'node'}</option>
              {catalog.filter(node => key !== 'event' || node.trigger).map(node => <option key={node.name}>{node.name}</option>)}
            </select>{step.use && <><p>{node?.doc}</p>
            <Messages items={fresh ? problems(report, '(inline)/' + key, 'use') : []} />
            {Object.entries(ports).map(([name, port]) => <div className="port" key={name}>
              <label>{name}<small>{type(port)}</small></label>
              <Field port={port} value={step.in[name]} catalog={catalog} onChange={next => edit(set(draft, ['steps', key, 'in', name], next))} />
              {key === 'run' && <select aria-label={'Event output for ' + name} value="" onChange={event => edit(set(draft, ['steps', key, 'in', name], '=event.' + event.target.value))}>
                <option value="" disabled>Use event output</option>{Object.keys(event?.outputs || {}).map(name => <option key={name}>{name}</option>)}
              </select>}
              <Messages items={fresh ? problems(report, '(inline)/' + key, name) : []} />
            </div>)}</>}
          </section>;
        })}
      </div>
      {reason && <p>{reason}</p>}
    </form>
  </section>;
}
