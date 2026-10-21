import { clone, validate } from './model.mjs';
export const typed = {
  name: 'typed-tree',
  encode(deck) { return clone(validate(deck)); },
  project(state) { return clone(validate(state)); },
  normalize(state) { return clone(validate(state)); },
  patch(state, slideID, elementID, changes) {
    const next = clone(state); const target = next.slides.find(s => s.id === slideID).elements.find(e => e.id === elementID);
    Object.assign(target, changes); next.version++; return validate(next);
  },
  reorder(state, order) { const next = clone(state); next.slides = order.map(id => next.slides.find(s => s.id === id)); next.version++; return validate(next); },
};
