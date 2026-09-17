// Browser and Bridge App transfer adapters share the same job protocol.  The
// native adapter is intentionally message based so large files never enter
// the React Native JavaScript heap.

const hostCapabilities = () => window.AidenHostCapabilities || {};

export function hasNativeTransfer() {
  const caps = hostCapabilities();
  return !!(caps.nativeStreaming && window.ReactNativeWebView && Number(caps.backupTransferVersion) >= 1);
}

export function hasNativeFilePicker() {
  const caps = hostCapabilities();
  return hasNativeTransfer() && !!caps.nativeFilePicker;
}

function postToHost(message) {
  if (!window.ReactNativeWebView) return false;
  window.ReactNativeWebView.postMessage(JSON.stringify(message));
  return true;
}

export function requestNativeFile() {
  if (!hasNativeFilePicker()) return false;
  return postToHost({type: 'aiden_restore_pick_file'});
}

export function requestNativeDownload(payload) {
  if (!hasNativeTransfer()) return false;
  return postToHost({type: 'aiden_backup_download', ...payload});
}

export function requestNativeUpload(payload) {
  if (!hasNativeTransfer()) return false;
  return postToHost({type: 'aiden_restore_upload', ...payload});
}

export function cancelNativeTransfer(jobId) {
  if (!hasNativeTransfer()) return false;
  return postToHost({type: 'aiden_transfer_cancel', job_id: jobId});
}

// The browser's download manager receives the archive straight from the
// device; the page never holds the archive in memory.
export function triggerBrowserDownload(url, filename) {
  const link = document.createElement('a');
  link.href = url;
  if (filename) link.download = filename;
  link.rel = 'noopener';
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  setTimeout(() => link.remove(), 1000);
}

export const ARCHIVE_MAGIC = 'AIDENBKP';
const HEADER_PREFIX_LENGTH = 14;
const MAX_PUBLIC_HEADER = 64 * 1024;

// readArchiveHeader reads only the fixed prefix and the public header JSON so
// the device can bound the KDF parameters before any chunk is uploaded.
export async function readArchiveHeader(file) {
  const prefix = new Uint8Array(await file.slice(0, HEADER_PREFIX_LENGTH).arrayBuffer());
  if (prefix.length < HEADER_PREFIX_LENGTH || new TextDecoder().decode(prefix.slice(0, 8)) !== ARCHIVE_MAGIC) {
    throw new Error('not_aiden_backup');
  }
  const view = new DataView(prefix.buffer, prefix.byteOffset, prefix.byteLength);
  if (view.getUint16(8) !== 1) throw new Error('unsupported_format');
  const headerLength = view.getUint32(10);
  if (!headerLength || headerLength > MAX_PUBLIC_HEADER) throw new Error('manifest_invalid');
  const body = new Uint8Array(await file.slice(HEADER_PREFIX_LENGTH, HEADER_PREFIX_LENGTH + headerLength).arrayBuffer());
  if (body.length !== headerLength) throw new Error('archive_truncated');
  return JSON.parse(new TextDecoder().decode(body));
}

async function sha256Hex(bytes) {
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  return Array.from(new Uint8Array(digest), value => value.toString(16).padStart(2, '0')).join('');
}

// uploadBrowserChunks reads one File.slice at a time and waits for the device
// to finish authenticating and staging that block before reading the next.
// onResponse may return a promise; the upload pauses until it resolves (used
// while the user reviews the manifest and submits the plan).
export async function uploadBrowserChunks(file, chunkSize, sendChunk, onResponse) {
  const size = Number(chunkSize) > 0 ? Number(chunkSize) : 4 * 1024 * 1024;
  let offset = 0;
  let index = 0;
  let last = null;
  while (offset < file.size) {
    const end = Math.min(file.size, offset + size);
    const chunk = new Uint8Array(await file.slice(offset, end).arrayBuffer());
    const hash = await sha256Hex(chunk);
    last = await sendChunk(index, chunk, hash);
    offset = end;
    index += 1;
    if (onResponse) await onResponse(last, offset, file.size);
  }
  return last;
}
