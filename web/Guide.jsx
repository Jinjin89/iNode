import { marked } from 'marked';

export function Guide({ text, onClose }) {
  return <aside className="guide" aria-label="Guide">
    <header><h2>Guide</h2><button className="quiet" onClick={onClose}>Close guide</button></header>
    {/* Only the embedded tutorial is rendered as HTML, never project content. */}
    <div className="tutorial" dangerouslySetInnerHTML={{ __html: marked.parse(text) }} />
  </aside>;
}
