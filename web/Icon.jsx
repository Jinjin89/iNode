const paths = {
  run: 'm8 5 11 7-11 7Z',
  undo: 'M8 4 4 8l4 4M4 8h9a6 6 0 0 1 0 12',
  redo: 'm16 4 4 4-4 4m4-4h-9a6 6 0 0 0 0 12',
  close: 'm6 6 12 12M6 18 18 6',
  delete: 'M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 10v7m4-7v7',
  up: 'm6 14 6-6 6 6',
  down: 'm6 10 6 6 6-6',
  add: 'M12 5v14M5 12h14',
  copy: 'M9 9h11v11H9ZM15 9V4H4v11h5',
};

export function Icon({ name }) {
  return <svg className="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <title>{name}</title><path d={paths[name]} />
  </svg>;
}
