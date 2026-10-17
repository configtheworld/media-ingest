// Collect files from drag-and-drop (including folders), file pickers and
// folder pickers. Only names and sizes are inspected; nothing is uploaded.

export function isJunk(name) {
  const base = name.split(/[\\/]/).pop();
  const lower = base.toLowerCase();
  return base.startsWith('._') || ['.ds_store', 'thumbs.db', 'desktop.ini', '.localized'].includes(lower) ||
    lower.startsWith('.spotlight-') || lower === '.trashes' || lower === '.fseventsd';
}

export function extOf(name) {
  const base = name.split(/[\\/]/).pop();
  const i = base.lastIndexOf('.');
  return i > 0 && i < base.length - 1 ? base.slice(i + 1).toLowerCase() : '';
}

export function stemOf(name) {
  const base = name.split(/[\\/]/).pop();
  const i = base.lastIndexOf('.');
  return i > 0 ? base.slice(0, i) : base;
}

// Returns {accepted: [{file, path}], ignored: [{name, reason}]}.
export function filterFiles(list, allowedExts) {
  const accepted = [];
  const ignored = [];
  for (const { file, path } of list) {
    const name = path || file.name;
    if (isJunk(name) || isJunk(file.name)) { ignored.push({ name, reason: 'junk' }); continue; }
    if (!allowedExts.has(extOf(file.name))) { ignored.push({ name, reason: 'extension' }); continue; }
    if (file.size === 0) { ignored.push({ name, reason: 'empty' }); continue; }
    accepted.push({ file, path: name });
  }
  return { accepted, ignored };
}

// Read a DataTransfer, recursing into dropped folders where supported.
export async function filesFromDataTransfer(dt) {
  const entries = [];
  for (const item of dt.items || []) {
    if (item.kind !== 'file') continue;
    const entry = item.webkitGetAsEntry?.();
    if (entry) entries.push(entry);
    else { const f = item.getAsFile(); if (f) entries.push({ plainFile: f }); }
  }
  if (!entries.length) return Array.from(dt.files || [], (file) => ({ file, path: file.name }));
  const out = [];
  for (const e of entries) await walk(e, '', out);
  return out;
}

async function walk(entry, prefix, out) {
  if (entry.plainFile) { out.push({ file: entry.plainFile, path: entry.plainFile.name }); return; }
  if (entry.isFile) {
    const file = await new Promise((res, rej) => entry.file(res, rej));
    out.push({ file, path: prefix + entry.name });
    return;
  }
  if (entry.isDirectory) {
    if (entry.name.startsWith('.')) return;
    const reader = entry.createReader();
    for (;;) {
      // readEntries returns results in batches until it returns an empty list.
      const batch = await new Promise((res, rej) => reader.readEntries(res, rej));
      if (!batch.length) break;
      for (const child of batch) await walk(child, `${prefix}${entry.name}/`, out);
    }
  }
}

export function fromInput(input) {
  return Array.from(input.files || [], (file) => ({ file, path: file.webkitRelativePath || file.name }));
}

// Fingerprint used to match re-selected files after a page reload.
export function fingerprint(file) {
  return `${file.name}|${file.size}|${file.lastModified}`;
}
