// Wolges leave worker: loads wolges-wasm pkg and a .klv2 file, serves leaveValue queries
let wasmReady = false;
let leaveApi = null;

async function initWolges() {
  if (wasmReady) return;
  // Adjust these paths if your pkg names differ
  const mod = await import('/wolges/pkg/wolges_wasm.js').catch(()=>null);
  if (!mod) throw new Error('wolges_wasm.js not found. Copy wolges-wasm/pkg to /wolges/pkg');
  if (typeof mod.default === 'function') {
    // Use object init to avoid deprecation warning
    await mod.default({ module_or_path: '/wolges/pkg/wolges_wasm_bg.wasm' });
  }
  if (!mod || typeof mod.load_klv2 !== 'function' || typeof mod.leave_value !== 'function') {
    throw new Error('wolges-wasm missing expected functions');
  }
  leaveApi = mod;
  wasmReady = true;
}

async function loadKLV2(url) {
  await initWolges();
  const resp = await fetch(url);
  if (!resp.ok) throw new Error('failed to fetch klv2');
  const buf = await resp.arrayBuffer();
  await leaveApi.load_klv2(new Uint8Array(buf));
  return true;
}

self.onmessage = async (e) => {
  const { type, payload } = e.data || {};
  try {
    if (type === 'init') {
      await initWolges();
      self.postMessage({ type:'init:ok' });
    } else if (type === 'loadKLV2') {
      await loadKLV2(payload && payload.url);
      self.postMessage({ type:'loadKLV2:ok' });
    } else if (type === 'leaveValue') {
      if (!wasmReady) throw new Error('not ready');
      const key = (payload && payload.leave) || '';
      const val = leaveApi.leave_value(key);
      self.postMessage({ type:'leaveValue:ok', payload:{ leave:key, value: val } });
    }
  } catch (err) {
    self.postMessage({ type: type+':err', error: String(err && err.message || err) });
  }
};
