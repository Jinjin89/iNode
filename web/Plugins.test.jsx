import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it, vi } from 'vitest';

vi.mock('./Canvas', () => ({ Dialog: () => null, Field: () => null, useReply: () => {} }));
vi.mock('./client', () => ({ call: vi.fn(), op: {} }));
import { Plugins } from './Plugins';

it('uses source labels supplied by the backend and reads naturally without adapters', () => {
  const render = sources => renderToStaticMarkup(<Plugins sources={sources} pending={[]} catalog={[]} changed={{}} />);
  expect(render([])).toContain('Enter a folder or a git URL to see what it will do before installing.');
  expect(render(['Example source', 'Another source'])).toContain('Enter a folder or a git URL, or another source (Example source, Another source) to see what it will do before installing.');
});
