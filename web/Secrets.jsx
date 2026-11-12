import { useState } from 'react';
import { useReply } from './Canvas';
import { call, op } from './client';

export function Secrets({ perform, busy, fail }) {
  const [names, setNames] = useState([]);
  const [name, setName] = useState('');
  const [value, setValue] = useState('');
  useReply(op['secret list'], [], setNames, fail);
  const change = (operation, ...args) => perform(async () => {
    for await (const _ of call(operation, ...args)) {}
    setValue('');
    for await (const names of call(op['secret list'])) setNames(names);
  });
  return <section className="secrets">
    <h1>Secrets</h1>
    <form className="row" onSubmit={event => { event.preventDefault(); change(op['secret set'], name, value); }}>
      <label>Name<input aria-label="Secret name" required value={name} onChange={event => setName(event.target.value)} /></label>
      <label>Value<input aria-label="Secret value" type="password" autoComplete="new-password" value={value} onChange={event => setValue(event.target.value)} /></label>
      <button disabled={busy} type="submit">Store secret</button>
    </form>
    {names.length === 0 ? <p>No secrets.</p> : <table>
      <thead><tr><th>Name</th><th /></tr></thead>
      <tbody>{names.map(name => <tr key={name}><td>{name}</td><td><button disabled={busy} onClick={() => change(op['secret remove'], name)}>Remove</button></td></tr>)}</tbody>
    </table>}
  </section>;
}
