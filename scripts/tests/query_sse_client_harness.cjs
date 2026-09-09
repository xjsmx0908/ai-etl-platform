"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const ts = require(path.resolve(__dirname, "../../web/node_modules/typescript"));

const root = path.resolve(__dirname, "../..");
const sourcePath = path.join(root, "web/lib/querySSE.ts");
const source = fs.readFileSync(sourcePath, "utf8");
const transpiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
  fileName: "querySSE.ts",
}).outputText;

const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "query-sse-"));
const tmpFile = path.join(tmpDir, "querySSE.cjs");
fs.writeFileSync(tmpFile, transpiled);
const { consumeQuerySSEStream } = require(tmpFile);

function encodeEvents(events) {
  const encoder = new TextEncoder();
  return encoder.encode(
    events
      .map((item) => `event: ${item.event}\ndata: ${JSON.stringify(item.data)}\n\n`)
      .join("")
  );
}

function hangingStream(firstChunk) {
  let pulled = false;
  return new ReadableStream({
    pull(controller) {
      if (!pulled) {
        pulled = true;
        controller.enqueue(firstChunk);
        return;
      }
      return new Promise(() => {});
    },
  });
}

async function withTimeout(promise, ms, label) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
  });
  try {
    return await Promise.race([promise, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

async function testDoneDoesNotWaitForEOF() {
  const received = { status: [], sources: [], deltas: [], done: [], errors: [] };
  const body = hangingStream(
    encodeEvents([
      { event: "status", data: { stage: "generating", message: "正在根据证据生成回答…", state: "running" } },
      { event: "sources", data: { sources: [{ doc_id: "d1" }] } },
      { event: "delta", data: { text: "日常巡检温度应记录。" } },
      { event: "done", data: { duration: "2.405s", token_usage: { total_tokens: 12 } } },
      { event: "delta", data: { text: "should-not-appear" } },
    ])
  );

  await withTimeout(
    consumeQuerySSEStream(body, {
      onStatus: (progress) => received.status.push(progress),
      onSources: (sources) => received.sources.push(sources),
      onDelta: (text) => received.deltas.push(text),
      onDone: (meta) => received.done.push(meta),
      onError: (message) => received.errors.push(message),
    }),
    500,
    "done-without-eof"
  );

  if (received.deltas.join("") !== "日常巡检温度应记录。") {
    throw new Error(`unexpected deltas: ${JSON.stringify(received.deltas)}`);
  }
  if (received.done.length !== 1 || received.done[0].duration !== "2.405s") {
    throw new Error(`expected one done meta, got ${JSON.stringify(received.done)}`);
  }
  if (received.errors.length !== 0) {
    throw new Error(`unexpected errors: ${JSON.stringify(received.errors)}`);
  }
}

async function testErrorDoesNotWaitForEOF() {
  const received = { errors: [], done: [] };
  const body = hangingStream(
    encodeEvents([{ event: "error", data: { error: "generation failed" } }])
  );

  await withTimeout(
    consumeQuerySSEStream(body, {
      onDone: (meta) => received.done.push(meta),
      onError: (message) => received.errors.push(message),
    }),
    500,
    "error-without-eof"
  );

  if (received.errors.join() !== "generation failed") {
    throw new Error(`unexpected errors: ${JSON.stringify(received.errors)}`);
  }
  if (received.done.length !== 0) {
    throw new Error(`error stream should not emit done, got ${JSON.stringify(received.done)}`);
  }
}

async function main() {
  await testDoneDoesNotWaitForEOF();
  await testErrorDoesNotWaitForEOF();
  console.log("ok");
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
