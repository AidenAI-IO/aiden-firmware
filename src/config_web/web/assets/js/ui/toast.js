/**
 * Transient status message.
 *
 * One toast at a time: a newer message replaces the current one rather than
 * stacking, which is what the native side does with a single overlay.
 */

import {el} from './dom.js';
import {resolve} from './text.js';

const VISIBLE_MS = 2600;

let currentNode = null;
let timer = null;

/**
 * Show a message.
 *
 * @param {import('./text.js').TextSpec} message
 * @param {object} [options]
 * @param {number} [options.durationMs]
 */
export function toast(message, options = {}) {
  dismiss();
  const node = el('div', {class: 'ds-toast', attrs: {role: 'status'}, text: resolve(message)});
  document.body.appendChild(node);
  currentNode = node;
  timer = setTimeout(dismiss, options.durationMs ?? VISIBLE_MS);
  return node;
}

/** Remove the visible toast, if any. */
export function dismiss() {
  if (timer) {
    clearTimeout(timer);
    timer = null;
  }
  if (currentNode && currentNode.parentNode) {
    currentNode.parentNode.removeChild(currentNode);
  }
  currentNode = null;
}
