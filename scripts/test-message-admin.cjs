// 浏览器验收：真实 Web、Worker、MySQL 和 Redis；仅模型使用可控 HTTP 服务。
// 用 PLAYWRIGHT_MODULE 指定已安装的 playwright 路径，不向生产前端添加依赖。
const assert = require("node:assert/strict");
const http = require("node:http");
const fs = require("node:fs/promises");
const path = require("node:path");
const os = require("node:os");
const { spawn } = require("node:child_process");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");

async function main() {
  const dsn = process.env.DEEPAGENT_TEST_MYSQL_DSN;
  const redis = process.env.DEEPAGENT_TEST_REDIS_ADDR;
  assert.ok(
    dsn && redis,
    "Set a dedicated DEEPAGENT_TEST_MYSQL_DSN and DEEPAGENT_TEST_REDIS_ADDR",
  );
  const directory = await fs.mkdtemp(
    path.join(os.tmpdir(), "deepagent-admin-browser-"),
  );
  const modelRequests = [];
  const checks = [];
  const processes = [];
  let worker;
  const modelServer = http.createServer(async (request, response) => {
    try {
      let body = "";
      for await (const chunk of request) body += chunk;
      const input = JSON.parse(body);
      modelRequests.push(input);
      const lastUser = input.messages.findLastIndex(
        (message) => message.role === "user",
      );
      const prompt = String(input.messages[lastUser]?.content || "");
      const completedTool = input.messages
        .slice(lastUser + 1)
        .some((message) => message.role === "tool");
      if (prompt.includes("触发失败")) {
        response.writeHead(400, { "content-type": "application/json" });
        response.end(
          JSON.stringify({
            error: {
              message: "受控模型请求失败",
              type: "invalid_request_error",
            },
          }),
        );
        return;
      }
      if (prompt.includes("等待取消")) {
        response.writeHead(200, { "content-type": "text/event-stream" });
        response.flushHeaders();
        const timer = setTimeout(() => response.end("data: [DONE]\n\n"), 30000);
        response.on("close", () => clearTimeout(timer));
        return;
      }
      let delta = {
        role: "assistant",
        content: prompt.includes("子任务内容") ? "子任务完成" : "已完成检查",
      };
      let finish = "stop";
      const call = (name, args) => ({
        role: "assistant",
        tool_calls: [
          {
            index: 0,
            id: "shared-call",
            type: "function",
            function: { name, arguments: JSON.stringify(args) },
          },
        ],
      });
      if (!completedTool && prompt.includes("检查启动命令")) {
        delta = call("read_file", { path: "README.md" });
        finish = "tool_calls";
      }
      if (!completedTool && prompt.includes("暂停确认")) {
        delta = call("ask_user", {
          question: "选择目录",
          options: ["src", "docs"],
        });
        finish = "tool_calls";
      }
      if (!completedTool && prompt.includes("创建子任务")) {
        delta = call("spawn_task", {
          title: "子任务界面验收",
          content: "子任务内容",
        });
        finish = "tool_calls";
      }
      if (completedTool && prompt.includes("暂停确认"))
        delta = { role: "assistant", content: "续跑完成，已收到 src" };
      response.writeHead(200, { "content-type": "text/event-stream" });
      for (const [value, reason] of [
        [delta, null],
        [{}, finish],
      ]) {
        response.write(
          "data: " +
            JSON.stringify({
              id: "fixture-response",
              object: "chat.completion.chunk",
              model: "fixture",
              choices: [{ index: 0, delta: value, finish_reason: reason }],
            }) +
            "\n\n",
        );
      }
      response.end("data: [DONE]\n\n");
    } catch (error) {
      response.writeHead(500);
      response.end(error.message);
    }
  });
  await new Promise((resolve) => modelServer.listen(0, "127.0.0.1", resolve));
  const modelURL = "http://127.0.0.1:" + modelServer.address().port + "/v1";
  const socket = http.createServer();
  await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
  const webAddress = "127.0.0.1:" + socket.address().port;
  await new Promise((resolve) => socket.close(resolve));
  const base = "http://" + webAddress;
  const configPath = path.join(directory, "config.yaml");
  await fs.writeFile(
    path.join(directory, "README.md"),
    "# 启动说明\ngo run ./cmd/deepagent_web\ngo run ./cmd/deepagent_worker\n",
  );
  await fs.writeFile(
    configPath,
    `manager:\n  mysql_dsn: ${JSON.stringify(dsn)}\n  redis_addr: ${JSON.stringify(redis)}\nworker:\n  concurrency: 2\n  scan_interval: 30ms\n  message_poll_interval: 30ms\n  idle_timeout: 200ms\n  shutdown_drain_timeout: 2s\n  shutdown_interrupt_drain_timeout: 2s\ndefault_model: fixture\nmodels:\n  - name: fixture\n    provider: openai\n    model: fixture\n    base_url: ${JSON.stringify(modelURL)}\n    api_key: test-only\n`,
  );
  async function start(binary, name, args) {
    const log = await fs.open(path.join(directory, name + ".log"), "a");
    const child = spawn(binary, args, { stdio: ["ignore", log.fd, log.fd] });
    await log.close();
    child.done = new Promise((resolve) => child.on("exit", resolve));
    processes.push(child);
    return child;
  }
  async function stop(child) {
    if (!child || child.exitCode !== null) return;
    child.kill("SIGTERM");
    const timeout = setTimeout(() => child.kill("SIGKILL"), 7000);
    await child.done;
    clearTimeout(timeout);
  }
  async function api(route, body) {
    const response = await fetch(
      base + route,
      body
        ? {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify(body),
          }
        : {},
    );
    const data = await response.json();
    assert.ok(response.ok, JSON.stringify(data));
    return data;
  }
  async function waitFor(check, description, timeout = 18000) {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
      const value = await check();
      if (value) return value;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error("Timed out: " + description);
  }
  const session = "admin-browser-" + Date.now();
  let browser;
  try {
    await start(
      process.env.DEEPAGENT_TEST_WEB || path.resolve("admin-web"),
      "web",
      ["--config", configPath, "--root", directory, "--addr", webAddress],
    );
    await waitFor(async () => {
      try {
        return (await fetch(base + "/admin/")).ok;
      } catch {
        return false;
      }
    }, "web startup");
    worker = await start(
      process.env.DEEPAGENT_TEST_WORKER || path.resolve("admin-worker"),
      "worker",
      ["--config", configPath],
    );
    browser = await chromium.launch({
      executablePath:
        process.env.CHROME_PATH ||
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      headless: true,
    });
    const context = await browser.newContext({
      viewport: { width: 1600, height: 1050 },
      permissions: ["clipboard-read", "clipboard-write"],
      reducedMotion: "reduce",
    });
    const page = await context.newPage();
    const browserErrors = [];
    page.on("pageerror", (error) => browserErrors.push(error.message));
    await page.goto(base + "/admin/");
    assert.equal(await page.locator("#empty").isVisible(), true);
    checks.push("空状态与真实内嵌资源");
    await page.goto(base + "/");
    await page.evaluate((value) => {
      localStorage.session = value;
      localStorage.language = "zh";
    }, session);
    await page.reload();
    await page.locator("#input").fill("检查启动命令");
    await page.locator("#send").click();
    const thread = await waitFor(
      async () => (await api("/api/threads?session_id=" + session))[0],
      "task creation",
    );
    const id = thread.id;
    await waitFor(async () => {
      const rows = await api("/api/threads/" + id + "/events");
      return rows.find(
        (row) => row.kind === "assistant" && row.text === "已完成检查",
      );
    }, "tool execution and response");
    await page.locator("#openAdmin").click();
    const admin = await waitFor(
      () => context.pages().find((candidate) => candidate !== page),
      "admin popup",
    );
    admin.on("pageerror", (error) => browserErrors.push(error.message));
    await admin.waitForLoadState();
    assert.equal(new URL(admin.url()).searchParams.get("id"), id);
    await admin.locator('[data-kind="model_request"]').first().waitFor();
    const inspection = await api("/api/admin/messages?kind=thread&id=" + id);
    assert.equal(typeof inspection.thread.id, "string");
    const tool = inspection.messages.find((message) => message.kind === "tool");
    const modelInput = inspection.messages.find(
      (message) => message.kind === "model_request",
    );
    assert.ok(
      tool &&
        tool.payload.arguments_json.includes("README.md") &&
        tool.payload.result_json.includes("启动说明"),
    );
    assert.ok(modelInput.payload.llm_response_id);
    const expected = modelRequests.find((input) =>
      input.messages.some(
        (message) =>
          message.role === "user" &&
          String(message.content).includes("检查启动命令"),
      ),
    );
    assert.deepEqual(
      modelInput.payload.messages.map((message) => ({
        role: message.role,
        content: message.content,
      })),
      expected.messages.map((message) => ({
        role: message.role,
        content: message.content,
      })),
    );
    assert.ok(
      inspection.messages.some(
        (message) => message.kind === "assistant" && message.payload.message,
      ),
    );
    assert.ok(
      inspection.messages.some(
        (message) =>
          message.kind === "model_response" &&
          message.payload.chunks.some((chunk) => chunk.tool_calls?.length),
      ),
    );
    assert.ok(
      modelInput.payload.tools.some((tool) => tool.name === "read_file"),
    );
    checks.push(
      "浏览器发送真实输入 → Worker → 模型 HTTP → 文件工具 → 请求与完整输出持久化 → 后台查询",
    );
    await admin.locator("#filters button").filter({ hasText: "工具" }).click();
    assert.equal(await admin.locator(".message").count(), 1);
    await admin.locator('[data-kind="tool"] details summary').click();
    assert.ok(
      (await admin.locator('[data-kind="tool"] pre').allTextContents())
        .join(" ")
        .includes("README.md"),
    );
    await admin.locator("#filters button").filter({ hasText: "全部" }).click();
    await admin.locator("#copyThread").click();
    assert.equal(
      await admin.evaluate(() => navigator.clipboard.readText()),
      id,
    );
    const downloadPromise = admin.waitForEvent("download");
    await admin.locator("#export").click();
    const download = await downloadPromise;
    const exported = JSON.parse(
      await fs.readFile(await download.path(), "utf8"),
    );
    assert.equal(exported.thread.id, id);
    await admin.locator("#language").click();
    assert.equal(await admin.locator("html").getAttribute("lang"), "en");
    assert.match(await admin.locator("#refresh").innerText(), /Refresh/);
    await admin.locator("#language").click();
    checks.push("类型过滤、请求/结果展开、精确复制长 ID、JSON 导出、中英切换");
    await admin.locator("#queryKind").selectOption("run");
    await admin.locator("#queryID").fill(tool.run_id);
    await admin.locator("#searchForm button").click();
    await waitFor(
      async () =>
        (await admin.locator(".message").count()) > 0 &&
        !(await admin.locator("#notice").isVisible()),
      "run lookup",
    );
    assert.equal(await admin.locator(".run-group").count(), 1);
    await admin.locator("#queryKind").selectOption("message");
    await admin.locator("#queryID").fill(tool.id);
    await admin.locator("#searchForm button").click();
    await admin.locator(".is-focused").waitFor();
    assert.equal(
      await admin.locator(".is-focused").getAttribute("data-message-id"),
      tool.id,
    );
    checks.push("执行 ID 查询、消息 ID 定位与上下文");
    await page.locator("#input").fill("暂停确认");
    await page.locator("#send").click();
    const blocked = await waitFor(async () => {
      const rows = await api("/api/threads/" + id + "/events");
      return rows.findLast(
        (row) =>
          row.kind === "interrupt" && row.payload.info?.question === "选择目录",
      );
    }, "follow-up block");
    await stop(worker);
    worker = await start(
      process.env.DEEPAGENT_TEST_WORKER || path.resolve("admin-worker"),
      "worker-restarted",
      ["--config", configPath],
    );
    await api("/api/threads/" + id + "/messages", {
      resume: {
        run_id: blocked.run_id,
        checkpoint_id: blocked.payload.checkpoint_id,
        interrupt_id: blocked.payload.interrupt_id,
        interrupt: { kind: "follow_up", data: { user_answer: "src" } },
      },
    });
    await waitFor(
      async () =>
        (await api("/api/threads/" + id + "/events")).some(
          (row) => row.text === "续跑完成，已收到 src",
        ),
      "resume after restart",
    );
    const resumed = await api(
      "/api/admin/messages?kind=run&id=" + blocked.run_id,
    );
    assert.ok(
      resumed.messages.some((message) => message.kind === "resume_run"),
    );
    assert.ok(
      resumed.messages.some(
        (message) =>
          message.kind === "assistant" &&
          message.payload.message?.content === "续跑完成，已收到 src",
      ),
    );
    assert.ok(
      resumed.runs.some(
        (run) => run.id === blocked.run_id && run.status === "finished",
      ),
    );
    checks.push("暂停、Worker 重启、原 Run 续跑，审批信息与恢复输入可查询");
    await page.locator("#input").fill("创建子任务");
    await page.locator("#send").click();
    const child = await waitFor(
      async () =>
        (await api("/api/threads?session_id=" + session)).find(
          (row) => row.parent_thread_id === id,
        ),
      "spawned child",
    );
    await waitFor(
      async () =>
        (await api("/api/threads/" + child.id + "/events")).some(
          (row) => row.text === "子任务完成",
        ),
      "child completion",
    );
    await admin.goto(base + "/admin/?kind=thread&id=" + id);
    await admin.locator('[data-kind="input"]').first().waitFor();
    assert.equal(await admin.locator(".run-thread").count(), 0);
    await admin.locator("#includeSubtasks").check();
    await admin.locator(".run-thread").first().waitFor();
    await admin.locator("#queryKind").selectOption("run");
    await admin.locator("#queryID").fill(tool.run_id);
    await admin.locator("#searchForm button").click();
    await admin.waitForFunction(() => document.querySelector("#notice").hidden);
    await admin
      .locator("#recent button")
      .filter({ has: admin.locator("small", { hasText: "任务 ID · " + id }) })
      .click();
    await admin.locator(".run-thread").first().waitFor();
    assert.equal(await admin.locator("#includeSubtasks").isChecked(), true);
    const descendants = await api(
      "/api/admin/messages?kind=thread&id=" + id + "&include_subtasks=true",
    );
    assert.ok(
      descendants.messages.some((message) => message.thread_id === child.id),
    );
    checks.push("实际创建子任务、父子隔离、包含子任务查询");
    await page.locator("#input").fill("触发失败");
    await page.locator("#send").click();
    await waitFor(
      async () =>
        (await api("/api/threads/" + id + "/events")).some(
          (row) => row.kind === "error",
        ),
      "model failure",
    );
    await admin.locator("#refresh").click();
    await admin.locator('[data-kind="error"]').first().waitFor();
    assert.ok(
      (await admin.locator('[data-kind="error"]').first().innerText()).includes(
        "受控模型请求失败",
      ),
    );
    await admin.screenshot({
      path: path.join(directory, "admin-desktop.png"),
      fullPage: true,
    });
    await admin.locator("#queryID").fill("9223372036854775807");
    await admin.locator("#searchForm button").click();
    await waitFor(
      async () =>
        (await admin.locator("#notice").innerText()).includes("未找到"),
      "not found",
    );
    assert.equal(await admin.locator("#results").isVisible(), false);
    checks.push("模型失败可追踪、未找到 ID、错误恢复");
    await stop(worker);
    const pending = await api("/api/threads", {
      session_id: session,
      name: '<img src=x onerror="window.injected=true">',
    });
    for (let index = 0; index < 105; index++)
      await api("/api/threads/" + pending.id + "/messages", {
        text: "待处理 " + index,
      });
    await admin.goto(base + "/admin/?kind=thread&id=" + pending.id);
    await admin.locator(".message").first().waitFor();
    assert.equal(await admin.locator(".message").count(), 100);
    assert.equal(await admin.locator("#loadMore").isVisible(), true);
    await admin.locator("#loadMore").click();
    await waitFor(
      async () => (await admin.locator(".message").count()) === 105,
      "pagination",
    );
    assert.equal(await admin.locator("#loadMore").isVisible(), false);
    assert.equal(await admin.evaluate(() => window.injected), undefined);
    assert.equal(await admin.locator("#taskTitle img").count(), 0);
    await admin.reload();
    await admin.locator(".message").first().waitFor();
    assert.ok(
      (await admin.locator("#recent").innerText()).includes(pending.id),
    );
    checks.push(
      "105 条待处理输入、跨页追加、无 Run 输入分组、最近查询持久化、用户文本防注入",
    );
    await admin.route("**/api/admin/messages?**", (route) =>
      route.abort("failed"),
    );
    await admin.locator("#refresh").click();
    await waitFor(
      async () =>
        (await admin.locator("#notice").innerText()).includes("无法连接"),
      "network error",
    );
    await admin.unroute("**/api/admin/messages?**");
    await admin.locator("#refresh").click();
    await admin.locator(".message").first().waitFor();
    assert.deepEqual(browserErrors, []);
    checks.push("网络故障提示与重试、无浏览器运行时异常");
    await fs.writeFile(
      path.join(directory, "acceptance.json"),
      JSON.stringify(
        {
          passed: checks,
          modelRequests: modelRequests.length,
          base,
          directory,
        },
        null,
        2,
      ),
    );
    console.log(JSON.stringify({ passed: checks, directory }, null, 2));
  } finally {
    if (browser) await browser.close();
    for (const child of processes.reverse()) await stop(child);
    await new Promise((resolve) => modelServer.close(resolve));
  }
}
main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
