/**
 * Messages to the companion app hosting this page in a WebView.
 *
 * The app draws the navigation bar natively, so a page reports what that bar
 * should show — title, whether there is something to save, and what saving
 * will need — and the app sends `aiden:save-request` back when its save
 * button is tapped. A standalone browser has no bridge and ignores all of it.
 *
 * Message types:
 *   - `aiden_config_save_state` {title, dirty, apply, action_label}; `title`
 *     and `action_label` arrive translated into the device's language
 *   - `aiden_config_restart` {apply, phase: 'started' | 'finished' | 'failed'}
 */

/**
 * @param {object} message
 * @param {Window} [win] - injectable for tests.
 */
export function postToHost(message, win = typeof window === 'undefined' ? null : window) {
  try {
    win?.ReactNativeWebView?.postMessage(JSON.stringify(message));
  } catch (error) {
    // A host that rejects the message must not break the page.
    console.warn('[settings] host message failed:', error && error.message);
  }
}
