import { marked } from 'marked';
import { Icon } from './Icon';
import { tip } from './model';

export function Guide({ text, onClose }) {
  return <aside className="guide" aria-label="Guide">
    <header><h2>Guide</h2><button className="quiet" onClick={onClose} title={tip('close', 'Close guide')}><Icon name="close" />Close guide</button></header>
    {/* Only the embedded tutorial is rendered as HTML, never project content. */}
    <div className="tutorial" dangerouslySetInnerHTML={{ __html: marked.parse(text) }} />
  </aside>;
}
