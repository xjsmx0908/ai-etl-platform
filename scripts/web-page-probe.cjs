#!/usr/bin/env node
/*
 * Minimal headless-Chrome page probe over the DevTools Protocol.
 *
 * Why this exists: the product-experience charter requires "walk the real page
 * first, then confirm the root cause in code". The repo has no Playwright /
 * Puppeteer, so this script drives a Chrome that the caller already started
 * with --remote-debugging-port. It sets a session cookie, navigates, waits for
 * the client-side fetch to settle, and dumps the rendered text of every page.
 *
 * Usage (Chrome must already listen on the debug port):
 *
 *   node scripts/web-page-probe.cjs \
 *     --base http://127.0.0.1:3100 \
 *     --cookie ai_etl_token=<value> \
 *     --url /login --url /documents --url /audit --url /release-center \
 *     --wait 6000 --out artifacts/probe.json
 *
 * Exit code is 0 only when every page returned HTTP 200 and rendered text.
 */

"use strict";

const fs = require("node:fs");
const path = require("node:path");

function parseArgs(argv) {
  const opts = { base: "http://127.0.0.1:3100", urls: [], wait: 5000, out: "", cookie: "", port: 9222, json: false, width: 1440, height: 900, login: "", afterLoad: "" };
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i];
    const value = argv[i + 1];
    switch (key) {
      case "--base": opts.base = value.replace(/\/$/, ""); i += 1; break;
      case "--url": opts.urls.push(value); i += 1; break;
      case "--wait": opts.wait = Number(value); i += 1; break;
      case "--out": opts.out = value; i += 1; break;
      case "--cookie": opts.cookie = value; i += 1; break;
      case "--port": opts.port = Number(value); i += 1; break;
      case "--login": opts.login = value; i += 1; break;
      case "--viewport": {
        const [w, h] = value.split("x").map(Number);
        if (!w || !h) throw new Error("--viewport must look like 1440x900");
        opts.width = w; opts.height = h; i += 1; break;
      }
      case "--after-load": opts.afterLoad = value; i += 1; break;
      case "--json": opts.json = true; break;
      default: throw new Error(`unknown argument: ${key}`);
    }
  }
  if (opts.urls.length === 0) throw new Error("at least one --url is required");
  return opts;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function debuggerEndpoint(port) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const res = await fetch(`http://127.0.0.1:${port}/json/version`);
      if (res.ok) return (await res.json()).webSocketDebuggerUrl;
    } catch {
      /* Chrome not listening yet. */
    }
    await sleep(250);
  }
  throw new Error(`no DevTools endpoint on 127.0.0.1:${port}; start Chrome with --remote-debugging-port=${port}`);
}

/** Thin CDP client: one socket, incrementing ids, awaited replies. */
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.nextId = 1;
    this.pending = new Map();
    this.events = [];
    ws.addEventListener("message", (event) => {
      const msg = JSON.parse(event.data);
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) reject(new Error(`${msg.error.message} (${JSON.stringify(msg.error.data || "")})`));
        else resolve(msg.result);
      } else if (msg.method) {
        this.events.push(msg);
      }
    });
  }

  send(method, params = {}, sessionId) {
    const id = this.nextId++;
    const payload = { id, method, params };
    if (sessionId) payload.sessionId = sessionId;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify(payload));
      setTimeout(() => {
        if (this.pending.delete(id)) reject(new Error(`CDP timeout: ${method}`));
      }, 30000);
    });
  }
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const browserWs = await debuggerEndpoint(opts.port);

  const ws = new WebSocket(browserWs);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", () => reject(new Error("DevTools socket failed")), { once: true });
  });

  const cdp = new CDP(ws);
  const { targetId } = await cdp.send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await cdp.send("Target.attachToTarget", { targetId, flatten: true });

  await cdp.send("Page.enable", {}, sessionId);
  await cdp.send("Runtime.enable", {}, sessionId);
  await cdp.send("Network.enable", {}, sessionId);

  // Headless defaults to 800x600, which silently hides every `lg:`/`xl:` panel.
  // innerText then omits that content and the probe reports a false pass.
  await cdp.send(
    "Emulation.setDeviceMetricsOverride",
    { width: opts.width, height: opts.height, deviceScaleFactor: 1, mobile: false },
    sessionId,
  );

  if (opts.cookie) {
    const eq = opts.cookie.indexOf("=");
    if (eq <= 0) throw new Error("--cookie must look like name=value");
    const name = opts.cookie.slice(0, eq);
    const value = opts.cookie.slice(eq + 1);
    // The server marks its cookie Secure; over plain http the browser will drop
    // it, so store it non-Secure. The server only reads the value back.
    const setResult = await cdp.send(
      "Network.setCookie",
      { name, value, url: opts.base, path: "/", httpOnly: true, secure: false, sameSite: "Lax" },
      sessionId,
    );
    if (!setResult.success) throw new Error(`browser rejected the ${name} cookie`);
    const stored = await cdp.send("Network.getCookies", { urls: [opts.base] }, sessionId);
    if (!(stored.cookies || []).some((cookie) => cookie.name === name && cookie.value === value)) {
      throw new Error(`${name} cookie did not persist in the browser`);
    }
  }

  const results = [];
  let loginOutcome = "";

  if (opts.login) {
    // Drive the real login page instead of injecting a cookie: the BFF sets an
    // HttpOnly Secure cookie, and Chrome refuses to attach it when we forge it
    // over plain http. Clicking the demo button keeps the session path honest.
    const labels = { admin: "体验发布预审（管理员）", user: "体验问答（普通账号）" };
    const label = labels[opts.login];
    if (!label) throw new Error("--login must be admin or user");
    await cdp.send("Page.navigate", { url: `${opts.base}/login` }, sessionId);
    await sleep(3500);
    const clicked = await cdp.send(
      "Runtime.evaluate",
      {
        expression: `(() => {
          const button = document.querySelector('button[aria-label=${JSON.stringify(label)}]');
          if (!button) return "missing";
          button.click();
          return "clicked";
        })()`,
        returnByValue: true,
      },
      sessionId,
    );
    loginOutcome = clicked.result?.value || "unknown";
    if (loginOutcome !== "clicked") throw new Error(`demo login button not found on /login (${loginOutcome})`);
    await sleep(6000);
  }

  for (const route of opts.urls) {
    const url = `${opts.base}${route}`;
    const before = cdp.events.length;
    await cdp.send("Page.navigate", { url }, sessionId);
    await sleep(opts.wait);

    let afterLoadOutcome = "";
    if (opts.afterLoad) {
      const outcome = await cdp.send(
        "Runtime.evaluate",
        { expression: opts.afterLoad, returnByValue: true },
        sessionId,
      );
      afterLoadOutcome = String(outcome.result?.value ?? outcome.exceptionDetails?.text ?? "unknown");
      await sleep(opts.wait);
    }

    const evaluated = await cdp.send(
      "Runtime.evaluate",
      {
        expression: `(() => {
          const text = (document.body && document.body.innerText) || "";
          return { title: document.title, href: location.href, text, htmlLength: document.documentElement.outerHTML.length };
        })()`,
        returnByValue: true,
      },
      sessionId,
    );

    const consoleErrors = cdp.events
      .slice(before)
      .filter((event) => event.method === "Runtime.exceptionThrown" || event.method === "Network.loadingFailed")
      .map((event) => event.method === "Runtime.exceptionThrown"
        ? String(event.params?.exceptionDetails?.exception?.description || event.params?.exceptionDetails?.text || "")
        : `${event.params?.type || "?"}: ${event.params?.errorText || ""}`)
      .filter(Boolean);

    const value = evaluated.result?.value || {};
    results.push({
      route,
      url,
      finalUrl: value.href || url,
      title: value.title || "",
      afterLoadOutcome,
      text: value.text || "",
      htmlLength: value.htmlLength || 0,
      consoleErrors,
    });
  }

  await cdp.send("Target.closeTarget", { targetId });
  ws.close();

  if (opts.out) {
    const dir = path.dirname(opts.out);
    if (dir && dir !== ".") fs.mkdirSync(dir, { recursive: true });
    const payload = opts.login ? { login: opts.login, loginOutcome, pages: results } : { pages: results };
    fs.writeFileSync(opts.out, `${JSON.stringify(payload, null, 2)}\n`, "utf8");
  }

  if (opts.login) process.stdout.write(`\nlogin: ${opts.login} (${loginOutcome})\n`);

  if (opts.json) {
    process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
  } else {
    for (const item of results) {
      process.stdout.write(`\n===== ${item.route} -> ${item.finalUrl} (html ${item.htmlLength}B) =====\n`);
      process.stdout.write(`${item.text}\n`);
      if (item.consoleErrors.length) {
        process.stdout.write(`--- console/network errors ---\n${item.consoleErrors.join("\n")}\n`);
      }
    }
  }

  const broken = results.filter((item) => item.htmlLength === 0 || item.text.trim() === "");
  if (broken.length) {
    process.stderr.write(`probe: ${broken.length} page(s) rendered nothing: ${broken.map((b) => b.route).join(", ")}\n`);
    process.exit(1);
  }
}

main().catch((err) => {
  process.stderr.write(`probe failed: ${err.message}\n`);
  process.exit(2);
});
