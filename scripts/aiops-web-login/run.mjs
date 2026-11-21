import { chromium } from 'playwright';
import { maxInputBytes, parseInput } from './protocol.mjs';
import { runWorkflow, WorkflowError } from './workflow.mjs';

let browser;
let context;
let deadline;
let stopping = false;
let output = { status: 'unavailable' };

function stopBrowser() {
  stopping = true;
  void browser?.close().catch(() => {});
  const forcedExit = setTimeout(() => process.exit(1), 2800);
  forcedExit.unref();
}
process.once('SIGTERM', stopBrowser);

try {
  if (process.getuid?.() === 0) throw new WorkflowError('unavailable');
  const chunks = [];
  let length = 0;
  for await (const chunk of process.stdin) {
    length += chunk.length;
    if (length > maxInputBytes) throw new WorkflowError('blocked');
    chunks.push(chunk);
  }
  let input;
  try { input = parseInput(Buffer.concat(chunks).toString('utf8')); } catch { throw new WorkflowError('blocked'); }
  if (stopping) throw new WorkflowError('unavailable');
  deadline = setTimeout(stopBrowser, 47_000);
  browser = await chromium.launch({
    headless: true,
    chromiumSandbox: true,
    timeout: 9500,
    proxy: { server: input.proxy, bypass: '<-loopback>' },
    args: [
      '--disable-quic',
      '--force-webrtc-ip-handling-policy=disable_non_proxied_udp',
      '--disable-background-networking',
      '--disable-domain-reliability',
      '--disable-sync',
      '--disable-breakpad',
      '--disable-crash-reporter',
      '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1',
    ],
  });
  if (stopping) throw new WorkflowError('unavailable');
  context = await browser.newContext({
    acceptDownloads: false,
    serviceWorkers: 'block',
    permissions: [],
    ignoreHTTPSErrors: false,
  });
  output = await runWorkflow(context, input);
} catch (err) {
  // Browser errors can contain page/script text and input values. Never return
  // or log their raw messages to the app, user or model.
  if (err instanceof WorkflowError) output = { status: err.status };
} finally {
  clearTimeout(deadline);
  if (context) await context.close().catch(() => {});
  if (browser) await browser.close().catch(() => {});
}
process.stdout.write(JSON.stringify(stopping ? { status: 'unavailable' } : output) + '\n');
