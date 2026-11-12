const names: string[] = JSON.parse(document.getElementById('operations')!.textContent!);
export const op: Record<string, string> = Object.fromEntries(names.map(name => [name, name]));
const fragment = location.hash.slice(1);
if (fragment) {
  sessionStorage.setItem('inode.credential', fragment);
  history.replaceState(null, '', location.pathname + location.search);
}
const credential = sessionStorage.getItem('inode.credential') || '';

export function call(operation: string, ...args: any[]): AsyncGenerator<any> {
  const controller = new AbortController();
  const stream = (async function* () {
    if (!credential) throw new Error('Open this project with inode start.');
    const response = await fetch('/', {
      method: 'POST',
      signal: controller.signal,
      headers: { Authorization: 'Bearer ' + credential, 'Content-Type': 'application/json' },
      body: JSON.stringify([operation, ...args]),
    });
    if (!response.body) return;
    const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
    let pending = '';
    try {
      while (true) {
        const { value, done } = await reader.read();
        pending += value || '';
        let end: number;
        while ((end = pending.indexOf('\n')) >= 0) {
          const line = JSON.parse(pending.slice(0, end));
          pending = pending.slice(end + 1);
          if ('error' in line) throw new Error(line.error);
          yield line.value;
        }
        if (done) break;
      }
    } finally {
      await reader.cancel();
    }
  })();
  const finish = stream.return.bind(stream);
  stream.return = value => { controller.abort(); return finish(value); };
  return stream;
}
