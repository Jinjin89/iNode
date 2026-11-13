import { useEffect, useState } from 'react';
import { Icon } from './Icon';
import { box, canReference, copyText, duration, kind, nodeName, textPreview, type } from './model';

export function Duration({ record }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!record.started || record.finished) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [record.started, record.finished]);
  return <time className="duration">{duration(record, now)}</time>;
}

export function Value({ value, port = {}, path = [], onUse, level = 0, compact = false }) {
  const [open, setOpen] = useState(level === 0);
  const [all, showAll] = useState(false);
  const [copied, setCopied] = useState('');
  useEffect(() => { setCopied(''); showAll(false); }, [value]);
  if (compact) {
    const use = typeof value === 'string' ? value : value?.use;
    const text = box(port) && use !== undefined ? nodeName(use) : copyText(value);
    return <span className="value-preview" title={text}>{text}</span>;
  }
  const list = Array.isArray(value), tree = value !== null && typeof value === 'object';
  const entries = tree ? Object.entries(value) : [];
  const text = value === undefined ? 'Not available' : value === null ? 'null' : String(value);
  const preview = textPreview(text);
  const tagged = kind(port) && !['any', 'text', 'number', 'bool', 'object', 'list'].includes(kind(port));
  const actions = value !== undefined && <span className="field-actions">
    <button type="button" title="Copy value" onClick={async event => {
      event.preventDefault(); event.stopPropagation();
      try { await navigator.clipboard.writeText(copyText(value)); setCopied('Copied'); }
      catch (error) { setCopied(error.message); }
    }}><Icon name="copy" />{copied || 'Copy'}</button>
    {onUse && canReference(path) && <button type="button" title={'Use =' + path.join('.')} onClick={event => {
      event.preventDefault(); event.stopPropagation(); onUse(path);
    }}>Use this</button>}
  </span>;
  return <div className={'returned-value nodrag nopan nowheel' + (box(port) ? ' box' : '') + (tree ? '' : ' value-row')} onClick={event => event.stopPropagation()}>
    {tagged && <small className="value-type">{box(port) ? 'Box · ' : ''}{type(port)}</small>}
    {tree ? <details open={open} onToggle={event => setOpen(event.currentTarget.open)}>
      <summary className="value-row">{list ? 'List' : 'Object'} · {entries.length} {list ? 'items' : 'fields'} {actions}</summary>
      {open && <div className="value-children">{entries.slice(0, all ? undefined : 8).map(([key, child]) => <div className="value-child" key={key}>
        <span className="value-key">{key}</span><Value value={child} port={list ? port.item : port.fields?.[key]} path={[...path, key]} onUse={onUse} level={level + 1} />
      </div>)}{!all && entries.length > 8 && <button type="button" className="quiet show-all" onClick={() => showAll(true)}>Show all {entries.length}</button>}</div>}
    </details> : <>{actions}<div className="value-text">{all ? text : preview}</div>
      {!all && text !== preview && <button type="button" className="quiet show-all" onClick={() => showAll(true)}>Show all</button>}</>}
  </div>;
}
