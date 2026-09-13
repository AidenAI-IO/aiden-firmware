// Compatibility helpers for callers that render grouped settings in a custom
// container. The production page uses the existing metadata renderer so that
// provider pickers, visibility rules and section saves retain their behavior.
import {AGENT_SETTINGS_GROUPS} from './config-groups.js';

function valueAt(object, path) {
  return path.split('.').reduce((value, key) => value == null ? undefined : value[key], object);
}

export function renderGroupedAgentSettings(container, configData = {}) {
  container.replaceChildren();
  Object.values(AGENT_SETTINGS_GROUPS)
    .sort((left, right) => left.order - right.order)
    .forEach((group) => {
      const heading = document.createElement('h3');
      heading.textContent = group.title;
      heading.dataset.groupId = group.id;
      container.appendChild(heading);
      const fields = group.fields || Object.values(group.subsections || {}).flatMap((subsection) => subsection.fields || []);
      fields.forEach((field) => {
        const value = document.createElement('span');
        value.dataset.configField = field.path;
        value.textContent = String(valueAt(configData, field.path) ?? '');
        container.appendChild(value);
      });
    });
}

export function collectGroupFieldValues(groupId, root = document) {
  const group = AGENT_SETTINGS_GROUPS[groupId];
  if (!group) return {};
  const fields = group.fields || Object.values(group.subsections || {}).flatMap((subsection) => subsection.fields || []);
  return Object.fromEntries(fields.map((field) => [field.path, root.querySelector(`[data-config-field="${field.path}"]`)?.value ?? '']));
}

export function updateGroupWithConfig() {}
