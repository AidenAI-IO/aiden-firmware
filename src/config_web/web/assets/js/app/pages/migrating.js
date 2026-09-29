/**
 * Placeholder for a settings section that has not been migrated to the new
 * layout yet. The route stays reachable and points at the classic page, which
 * still owns that section's controls.
 */

import {el} from '../../ui/dom.js';
import {group, row, screen} from '../../ui/list.js';
import {icon} from '../../ui/icon.js';
import {msg, text} from '../../ui/text.js';

/**
 * @param {string} sectionId - label of the section still living on the classic page.
 */
export function migratingPage(sectionId) {
  return async function renderMigrating() {
    const link = el(
      'a',
      {class: 'ds-row ds-row--tappable', attrs: {href: '/legacy'}},
      [
        el('div', {class: 'ds-row__body'}, [
          text(msg('ui.migrating_action', '打开经典页面'), 'ds-row__label'),
        ]),
        el('div', {class: 'ds-row__accessory'}, [icon('chevron', {class: 'ds-row__chevron'})]),
      ],
    );

    return screen([
      group({
        rows: [
          row({
            label: msg('ui.migrating', '该分区尚未迁移到新布局。'),
            description: sectionId,
          }),
        ],
      }),
      el('div', {class: 'page__footer'}, [link]),
    ]);
  };
}
