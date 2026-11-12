import { useRef, useState } from 'react';
import { useReply } from './Canvas';
import { call, op } from './client';

export function Secrets({ perform, pending, ask, fail }) {
  const [names, setNames] = useState([]);
  const [name, setName] = useState('');
  const [value, setValue] = useState('');
  const latest = useRef({ name, value });
  latest.current = { name, value };
  useReply(op['secret list'], [], setNames, fail);
  const change = (operation, ...args) => perform(async () => {
    for await (const _ of call(operation, ...args)) {}
    if (operation === op['secret set'] && latest.current.name === args[0] && latest.current.value === args[1]) setValue('');
    for await (const names of call(op['secret list'])) setNames(names);
  }, 'secret:' + args[0]);
  const store = () => change(op['secret set'], name, value);
  return <section className="secrets">
    <h1>Secrets</h1><p>A secret is a stored value used by name; its value is never shown again.</p>
    {names.length === 0 && <p>Store a secret below, then choose its name in a node's secret input.</p>}
    <form className="screen-form" onSubmit={event => { event.preventDefault();
      if (names.includes(name)) ask('Replace secret?', 'Replace the stored value of “' + name + '”? Nodes using this name will receive the new value.', 'Replace secret', store); else store();
    }}>
      <header className="form-heading"><h2>Store a secret</h2><button className="primary" disabled={pending.includes('secret:' + name)} type="submit">{pending.includes('secret:' + name) ? 'Storing…' : names.includes(name) ? 'Replace secret' : 'Store secret'}</button></header><div className="row">
        <label>Name<input aria-label="Secret name" required value={name} onChange={event => setName(event.target.value)} /></label>
        <label>Value<input aria-label="Secret value" type="password" autoComplete="new-password" value={value} onChange={event => setValue(event.target.value)} /></label>
      </div>
    </form>
    {names.length > 0 && <div className="table-scroll"><table>
      <thead><tr><th>Name</th><th>Actions</th></tr></thead>
      <tbody>{names.map(name => <tr key={name} aria-busy={pending.includes('secret:' + name)}><td>{name}</td><td>
        <button disabled={pending.includes('secret:' + name)} onClick={() => ask('Remove secret?', 'Remove secret “' + name + '”? Nodes using it will report that it is missing.', 'Remove secret', () => change(op['secret remove'], name))}>{pending.includes('secret:' + name) ? 'Working…' : 'Remove'}</button>
      </td></tr>)}</tbody>
    </table></div>}
  </section>;
}
