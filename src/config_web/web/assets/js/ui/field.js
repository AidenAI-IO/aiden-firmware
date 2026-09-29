/**
 * Text field with the three states the design specifies: resting, focused and
 * invalid, each with an optional helper or error line underneath.
 */

import {el} from './dom.js';
import {resolve, text} from './text.js';

/**
 * @param {object} props
 * @param {import('./text.js').TextSpec} props.label
 * @param {string} [props.value]
 * @param {import('./text.js').TextSpec} [props.placeholder]
 * @param {string} [props.type] - input type, defaults to `text`.
 * @param {import('./text.js').TextSpec} [props.help]
 * @param {import('./text.js').TextSpec} [props.error] - initial error, marks the field invalid.
 * @param {string} [props.autocomplete]
 * @param {boolean} [props.autofocus]
 * @param {(value: string) => void} [props.onInput]
 * @param {(value: string) => void} [props.onSubmit]
 * @returns {{el: HTMLElement, input: HTMLInputElement, setError: (message: import('./text.js').TextSpec|null) => void, focus: () => void}}
 */
export function textField(props) {
  const input = el('input', {
    class: 'ds-input',
    attrs: {
      type: props.type || 'text',
      value: props.value ?? '',
      placeholder: props.placeholder ? resolve(props.placeholder) : null,
      autocomplete: props.autocomplete || 'off',
      autocapitalize: 'none',
      autocorrect: 'off',
      spellcheck: 'false',
      'aria-label': resolve(props.label),
    },
    data: {action: 'text-input'},
  });
  if (props.placeholder && typeof props.placeholder !== 'string') {
    input.setAttribute('data-i18n-placeholder', props.placeholder.key);
    if (props.placeholder.fallback != null) {
      input.setAttribute('data-i18n-placeholder-default', props.placeholder.fallback);
    }
  }
  if (props.onInput) {
    input.addEventListener('input', () => props.onInput(input.value));
  }
  if (props.onSubmit) {
    input.addEventListener('keydown', event => {
      if (event.key === 'Enter') props.onSubmit(input.value);
    });
  }

  const helpNode = props.help != null ? text(props.help, 'ds-field__help') : null;
  const errorNode = el('div', {class: 'ds-field__error', attrs: {'aria-live': 'polite'}});
  errorNode.hidden = true;

  const wrap = el('div', {class: 'ds-field'}, [
    text(props.label, 'ds-field__label'),
    input,
    helpNode,
    errorNode,
  ]);

  function setError(message) {
    if (message == null) {
      errorNode.hidden = true;
      errorNode.textContent = '';
      input.classList.remove('ds-input--invalid');
      input.removeAttribute('aria-invalid');
      if (helpNode) helpNode.hidden = false;
      return;
    }
    errorNode.textContent = resolve(message);
    errorNode.hidden = false;
    input.classList.add('ds-input--invalid');
    input.setAttribute('aria-invalid', 'true');
    if (helpNode) helpNode.hidden = true;
  }

  if (props.error != null) setError(props.error);

  return {
    el: wrap,
    input,
    setError,
    focus: () => {
      input.focus();
      if (props.autofocus) input.select();
    },
  };
}
