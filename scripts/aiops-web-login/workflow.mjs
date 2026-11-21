import { containsCredentialURL, hideCredentials, loginPayloadContains, maxText, maxTitle } from './protocol.mjs';

export class WorkflowError extends Error {
  constructor(status) { super(status); this.status = status; }
}
function stop(status) { throw new WorkflowError(status); }

function asURL(value) {
  try { return new URL(value); } catch { return null; }
}

/*
 * Only recognize an unambiguous, ordinary login form. Never interpret hints or
 * commands supplied by page text as instructions, try several forms, navigate
 * to discovered endpoints, or click an MFA/SSO/consent action.
 * This callback lives in the page and never receives credentials.
 */
function inspectLoginForm() {
  const visible = (el) => {
    if (!(el instanceof HTMLElement) || el.closest('template,[inert],[aria-hidden="true"]') ||
        el.getClientRects().length === 0) return false;
    return getComputedStyle(el).visibility === 'visible';
  };
  const inputs = [...document.querySelectorAll('input')].filter((el) =>
    visible(el) && !el.readOnly && !el.matches(':disabled'));
  const labels = (el) => [
    el.name, el.id, el.autocomplete, el.placeholder, el.getAttribute('aria-label'),
    ...[...(el.labels || [])].map((label) => label.innerText),
  ].filter(Boolean).join(' ');
  const challengeInput = inputs.some((el) =>
    /(?:one-time-code|\b(?:otp|totp|mfa|2fa|captcha)\b|验证码|验证代码|动态口令|(?:authentication|verification|security|one.time)\s+code)/i.test(labels(el)));
  const challengeFrame = [...document.querySelectorAll('iframe')].some((el) =>
    visible(el) && /captcha|turnstile|challenge-platform/i.test(el.getAttribute('src') || el.getAttribute('title') || ''));
  const challengeWidget = [...document.querySelectorAll('.g-recaptcha,.h-captcha,.cf-turnstile,[id="challenge-form"]')].some(visible);
  const headings = [...document.querySelectorAll('h1,h2,h3,[role="heading"],[role="alert"]')].filter(visible)
    .map((el) => el.innerText?.slice(0, 180)).filter(Boolean).join('\n').slice(0,1000);
  if (challengeInput || challengeFrame || challengeWidget ||
      /(?:enter (?:the |your )?(?:verification|authentication|security) code|verify (?:that )?you(?: are|'re) human|two.factor authentication|多重身份验证|双重身份验证|请输入验证码|扫码登录|安全验证)/i.test(headings)) {
    return { kind: 'verification' };
  }

  const pw = inputs.filter((el) => el.type === 'password');
  const isIdentity = (el) => {
    if (!['text','email','tel'].includes(el.type)) return false;
    if (/^(?:email|username)$/i.test(el.autocomplete || '') || el.type === 'email') return true;
    return /(?:^|[^a-z0-9])(?:user(?:[_-]?name|[_-]?id)?|account|login(?:[_-]?id)?|e[_-]?mail)(?:$|[^a-z0-9])|用户名|登录名|账号|帐户|账户|邮箱|邮件地址/.test(labels(el).toLowerCase());
  };
  if (pw.length > 1 || pw.some((el) => /new-password/i.test(el.autocomplete) || /confirm|确认/.test(labels(el).toLowerCase()))) {
    return { kind: 'ambiguous' };
  }

  const password = pw[0] || null;
  const identities = inputs.filter(isIdentity);
  const scoped = password?.form ? identities.filter((el) => el.form === password.form) : identities;
  if (scoped.length > 1) return { kind: 'ambiguous' };
  const username = scoped[0] || null;
  if (!username && !password) return { kind: 'none' };
  const form = password?.form || username?.form || null;
  const controls = [...document.querySelectorAll('button,input[type=submit],input[type=button]')]
    .filter((el) => visible(el) && !el.matches(':disabled') && el.form === form)
    .filter((el) => ['submit','button'].includes(el.type));
  const label = (el) => (el.innerText || el.value || el.getAttribute('aria-label') || '').trim().replace(/\s+/g, ' ');
  const loginLabel = /^(?:sign[ -]?in|log[ -]?in|登录|登入|登錄|登入帳戶|登录账号|立即登录|确认登录|確認登入)$/i;
  const nextLabel = /^(?:next|continue|下一步|继续|繼續)$/i;
  const buttons = controls.filter((el) => loginLabel.test(label(el)) || !password && nextLabel.test(label(el)));
  if (!password && buttons.length === 0) return { kind: 'none' };
  if (buttons.length !== 1) return { kind: 'ambiguous' };
  const button = buttons[0];
  if (form && button.type === 'submit') {
    const action = button.hasAttribute('formaction') ? button.formAction : form.action;
    const method = button.hasAttribute('formmethod') ? button.formMethod : form.method;
    const target = (button.hasAttribute('formtarget') ? button.formTarget : form.target) || '_self';
    // Do not put credentials in a native GET request or open another window.
    try {
      const destination = new URL(action, location.href);
      if (destination.origin !== location.origin || destination.username || destination.password ||
          method !== 'post' || target.toLowerCase() !== '_self') return { kind: 'blocked' };
    } catch { return { kind: 'blocked' }; }
  }
  return { kind: password ? 'password' : 'identity', username, password, button };
}

function inspectLoginEntrance() {
  const signInLabel = /^(?:sign[ -]?in|log[ -]?in|登录|登入|登錄|登录账号|立即登录)$/i;
  const destinations = new Set();
  let foreignLogin = false;
  for (const link of document.querySelectorAll('a[href]')) {
    if (!link.getClientRects().length || getComputedStyle(link).visibility !== 'visible' ||
        link.closest('[inert],[aria-hidden="true"]') || link.hasAttribute('download')) continue;
    if (!signInLabel.test((link.innerText || link.getAttribute('aria-label') || '').trim())) continue;
    let url;
    try { url = new URL(link.href); } catch { continue; }
    if (url.origin !== location.origin) { foreignLogin = true; continue; }
    if (url.username || url.password || url.search || url.hash || url.href === location.href) continue;
    // Follow only a single recognizable login link, not arbitrary links or
    // guessed endpoint paths. The login page still needs a safe form.
    if (/(?:^|[\\/_-])(?:log[_-]?in|sign[_-]?in|auth|account)(?:$|[\\/._-])/i.test(url.pathname)) {
      destinations.add(url.href);
    }
  }
  if (destinations.size === 1) return {href: [...destinations][0]};
  return {foreignLogin: destinations.size === 0 && foreignLogin};
}

async function inspect(page) {
  const handle = await page.evaluateHandle(inspectLoginForm);
  const kindProp = await handle.getProperty('kind');
  try { return { handle, kind: await kindProp.jsonValue() }; } finally { await kindProp.dispose(); }
}

async function waitForInitialForm(page, check, allowEntrance = false) {
  const until = Date.now() + 9000;
  do {
    check();
    // A navigation can replace the DOM while we inspect; start looking again
    // but never resubmit any credentials.
    const view = await inspect(page).catch(() => null);
    if (view) {
      if (view.kind === 'verification') stop('verification_required');
      if (view.kind === 'blocked') stop('blocked');
      if (view.kind === 'identity' || view.kind === 'password') return view;
      await view.handle.dispose();
      if (allowEntrance && view.kind === 'none') {
        const entry = await page.evaluate(inspectLoginEntrance).catch(() => null);
        if (entry?.href) return {kind:'link', href:entry.href};
        if (entry?.foreignLogin) stop('blocked');
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 160));
  } while (Date.now() < until);
  stop('unsupported_login');
}

async function withAction(view, gate, credential, fields) {
  const prop = async (name) => (await view.handle.getProperty(name)).asElement();
  const user = await prop('username');
  const password = await prop('password');
  const submit = await prop('button');
  if (!submit || (fields.needUser && !user) || (fields.needPassword && !password)) stop('unsupported_login');
  try {
    if (user) await user.fill(fields.username, { timeout: 4000 });
    if (password) await password.fill(fields.password, { timeout: 4000 });
    // Reactive pages may replace elements or change form action on input.
    // Validate identities and the real action again before the click.
    const page = submit.ownerFrame ? (await submit.ownerFrame())?.page() : null;
    if (!page) stop('blocked');
    const delivery = await page.evaluate(({u, p, b}) => {
      // Inline an independently checked gate based on the live DOM. No
      // webpage script source or model-controlled JavaScript is evaluated.
      if (!b?.isConnected || (u && (!u.isConnected || u.form !== (p?.form || b.form))) ||
          (p && (!p.isConnected || p.form !== b.form || p.type !== 'password'))) return null;
      let nativeAction = '';
      if (b.form && b.type === 'submit') {
        const method = b.hasAttribute('formmethod') ? b.formMethod : b.form.method;
        const destination = new URL(b.hasAttribute('formaction') ? b.formAction : b.form.action, location.href);
        const target = (b.hasAttribute('formtarget') ? b.formTarget : b.form.target) || '_self';
        if (method !== 'post' || destination.origin !== location.origin || destination.username ||
            destination.password || target.toLowerCase() !== '_self') return null;
        destination.hash = '';
        nativeAction = destination.href;
      }
      const roots = document.querySelectorAll('input[autocomplete="one-time-code"],.g-recaptcha,.h-captcha,.cf-turnstile');
      if ([...roots].some((el) => el.getClientRects().length > 0)) return null;
      return { nativeAction };
    }, { u: user, p: password, b: submit });
    if (!delivery) stop('blocked');
    const phase = { credential, nativeAction: delivery.nativeAction, request: null, responseCode: 0, failed: false };
    gate.phase = phase;
    await submit.click({ timeout: 5000, noWaitAfter: true });
    return phase;
  } finally {
    await view.handle.dispose().catch(() => {});
  }
}

async function readVisiblePage(page) {
  return page.evaluate((maximum) => {
    const excluded = 'form,input,textarea,script,style,template,noscript,button,[contenteditable],[inert],[aria-hidden="true"],[role="textbox"]';
    const main = [...document.querySelectorAll('main,[role="main"]')].find((el) => el.getClientRects().length > 0) || document.body;
    const pieces = [];
    let size = 0;
    if (!main) return { title: document.title, text: '' };
    const walker = document.createTreeWalker(main, NodeFilter.SHOW_TEXT);
    for (let node, count = 0; count < 10000 && (node = walker.nextNode()); count++) {
      const value = node.nodeValue?.trim();
      const parent = node.parentElement;
      if (!value || !parent || parent.closest(excluded) ||
          !parent.getClientRects().length || getComputedStyle(parent).visibility !== 'visible') continue;
      // Retain substantial lookahead beyond the displayed prefix for
      // boundary redaction without sending an unbounded DOM Text node.
      const excerpt = value.slice(0, maximum - size);
      pieces.push(excerpt);
      size += excerpt.length + 1;
      if (size >= maximum) break;
    }
    return { title: document.title, text: pieces.join('\n') };
  }, maxText + 16384);
}

/** One legitimate login and one read. This module never has model tools. */
export async function runWorkflow(context, input) {
  const gate = { phase: null, blockedNavigation: false };
  const page = await context.newPage();
  // Playwright's request route can get the first URL only for redirects.
  // Fail closed before a 307/308 can replay the credential-bearing POST.
  // Also inspect redirect destinations before a new navigation leaves this
  // exact origin. The pinned CONNECT proxy is the independent network guard.
  const responseGuard = await context.newCDPSession(page);
  responseGuard.on('Fetch.requestPaused', async (event) => {
    const { requestId, responseStatusCode: code, responseHeaders, request, resourceType } = event;
    try {
      const location = responseHeaders?.find(({name}) => name.toLowerCase() === 'location')?.value;
      const destination = location && code >= 300 && code < 400 ? new URL(location, request.url) : null;
      if ((code === 307 || code === 308) && !['GET', 'HEAD', 'OPTIONS'].includes(request.method) ||
          destination && (destination.origin !== input.origin || destination.username || destination.password ||
            containsCredentialURL(destination, input.password))) {
        if (resourceType === 'Document' || request.method === 'POST') gate.blockedNavigation = true;
        await responseGuard.send('Fetch.failRequest', { requestId, errorReason:'BlockedByClient' });
      } else {
        await responseGuard.send('Fetch.continueResponse', { requestId });
      }
    } catch {
      // A malformed redirect or closing target must not escape the guard.
      gate.blockedNavigation = true;
      await responseGuard.send('Fetch.failRequest', { requestId, errorReason:'BlockedByClient' }).catch(() => {});
    }
  });
  await responseGuard.send('Fetch.enable', { patterns:[{ urlPattern:'*', requestStage:'Response' }] });
  const rejectRequest = async (route, markNavigation = false) => {
    if (markNavigation) gate.blockedNavigation = true;
    await route.abort('blockedbyclient');
  };
  if (typeof context.routeWebSocket !== 'function') stop('unavailable');
  await context.routeWebSocket('**/*', (socket) => socket.close({ code: 1008 }));
  context.on('page', (child) => { if (child !== page) void child.close().catch(() => {}); });
  context.on('response', (response) => {
    if (gate.phase?.request === response.request()) gate.phase.responseCode = response.status();
  });
  context.on('requestfailed', (request) => {
    if (gate.phase?.request === request) gate.phase.failed = true;
  });
  await context.route('**/*', async (route) => {
    const req = route.request(), url = asURL(req.url());
    const mainFrame = (() => { try { return req.frame() === page.mainFrame(); } catch { return false; } })();
    const topLevel = req.isNavigationRequest() && mainFrame;
    if (!url || url.origin !== input.origin ||
        url.username || url.password || containsCredentialURL(url,input.password) ||
        (gate.phase && topLevel && req.method() === 'GET' && containsCredentialURL(url,input.username))) {
      await rejectRequest(route, topLevel); return;
    }
    if (['GET', 'HEAD', 'OPTIONS'].includes(req.method())) { await route.continue(); return; }
    // Allow a single POST from our confirmed form activation, and only when
    // its actual body contains the credential expected at that step.
    const active = gate.phase;
    // Native forms may only POST to the exact inspected action. For a
    // JS login button, require a recognizable authentication endpoint as
    // well as the expected credential, not a generic state-changing POST.
    const matchesLoginEndpoint = active && (active.nativeAction ? active.nativeAction === url.href :
      /(?:^|[\\/_-])(?:auth|log[_-]?in|sign[_-]?in|sessions?|identify|account|token|passport)(?:$|[\\/._-])/i.test(url.pathname));
    if (req.method() === 'POST' && mainFrame && active && !active.request && matchesLoginEndpoint &&
        loginPayloadContains(req.postData(), active.credential)) {
      active.request = req;
      await route.continue(); return;
    }
    await rejectRequest(route, topLevel);
  });
  const check = (allowBlank = false) => {
    if (gate.blockedNavigation || page.isClosed()) stop('blocked');
    const current = page.url(), url = asURL(current);
    if ((!allowBlank || current !== 'about:blank') && url?.origin !== input.origin) stop('blocked');
  };
  const goto = async (url) => {
    try {
      const response = await page.goto(url, { timeout: 12000, waitUntil: 'domcontentloaded' });
      check();
      if (!response || response.status() >= 400) stop(response?.status() === 401 || response?.status() === 403 ? 'login_failed' : 'unavailable');
    } catch (err) {
      if (err instanceof WorkflowError) throw err;
      check(true);
      stop('unavailable');
    }
  };

  await goto(input.url);
  let view = await waitForInitialForm(page, check, true);
  if (view.kind === 'link') {
    await goto(view.href);
    view = await waitForInitialForm(page, check);
  }
  let identityStep = false;
  if (view.kind === 'identity') {
    identityStep = true;
    const phase = await withAction(view, gate, input.username, { needUser:true, username:input.username });
    try {
      view = await waitForInitialForm(page, () => {
        check();
        if (phase.failed || phase.responseCode >= 400) stop('login_failed');
      });
      // Some shops render the old form while navigating; wait for the genuine
      // password step, never click "continue" on the same form again.
      const until = Date.now() + 7000;
      while (view.kind === 'identity' && Date.now() < until) {
        await view.handle.dispose();
        await new Promise((resolve) => setTimeout(resolve,160));
        view = await waitForInitialForm(page, check);
      }
      if (view.kind !== 'password') stop('login_failed');
    } finally { gate.phase = null; }
  }

  const phase = await withAction(view, gate, input.password, {
    needUser: !identityStep, needPassword: true, username: input.username, password: input.password,
  });
  let clearSince = 0, loaded = false;
  try {
    const until = Date.now() + 12500;
    do {
      check();
      if (phase.failed || phase.responseCode >= 400) stop('login_failed');
      const state = await inspect(page).catch(() => null);
      if (state) {
        if (state.kind === 'verification') stop('verification_required');
        if (state.kind === 'none' && phase.responseCode >= 200 && phase.responseCode < 400) {
          if (!clearSince) clearSince = Date.now();
          if (Date.now() - clearSince >= 700) { loaded = true; await state.handle.dispose(); break; }
        } else { clearSince = 0; }
        await state.handle.dispose();
      }
      await new Promise((resolve) => setTimeout(resolve,160));
    } while (Date.now() < until);
  } finally { gate.phase = null; }
  if (!loaded) stop('login_failed');

  if (input.target && page.url() !== input.target) await goto(input.target);
  check();
  const final = await inspect(page);
  await final.handle.dispose();
  if (final.kind === 'verification') stop('verification_required');
  if (final.kind !== 'none') stop('login_failed');
  const contents = await readVisiblePage(page);
  const secrets = [input.username, input.password];
  const text = hideCredentials(contents.text, secrets, maxText);
  if (!text) stop('unavailable');
  return { status: 'ok', title: hideCredentials(contents.title, secrets, maxTitle), text };
}
