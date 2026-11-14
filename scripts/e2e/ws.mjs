// Minimal WebSocket client for e2e scripts (Node 22+).
// node ws.mjs <url> <cookie> <mode> [args]
//   events <after> <untilType> [maxMs]   print events (JSON lines) until one of type untilType
//   term <input>                          run a terminal, send input when output appears, print output
const [, , url, cookie, mode, ...args] = process.argv;
const ws = new WebSocket(url, { headers: { Cookie: `agenthold_session=${cookie}`, Origin: new URL(url).origin.replace('ws', 'http') } });
ws.binaryType = 'arraybuffer';
const max = Number(args[2] ?? 60000);
setTimeout(() => { console.error('timeout'); process.exit(2); }, max);
let out = '', sent = false;
ws.onmessage = (e) => {
  if (mode === 'events') {
    if (typeof e.data !== 'string') return;
    const m = JSON.parse(e.data);
    if (!m.seq) return;
    console.log(JSON.stringify(m));
    if (m.type === args[1]) process.exit(0);
    if (args[3] && JSON.stringify(m).includes(args[3])) process.exit(0);
  } else {
    if (typeof e.data === 'string') {
      const m = JSON.parse(e.data);
      if (m.type === 'exit' || m.type === 'error') { console.log(out); console.log(JSON.stringify(m)); process.exit(0); }
      return;
    }
    out += new TextDecoder().decode(e.data);
    if (!sent && out.includes(args[0])) { sent = true; ws.send(new TextEncoder().encode(args[1] ?? '\n')); }
  }
};
ws.onerror = (e) => { console.error('ws error', e.message); process.exit(1); };
