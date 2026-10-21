"use strict";

function getCategory(kind) {
  if (kind === "input" || kind === "resume_run" || kind?.startsWith("control."))
    return "input";
  if (
    kind === "assistant" ||
    kind === "model_request" ||
    kind === "model_response"
  )
    return "model";
  if (kind === "tool" || kind === "error") return kind;
  return "system";
}

function getMessageText(message) {
  const payload = message.payload;
  if (typeof payload === "string") return payload;
  if (!payload) return "";
  if (typeof payload.message === "string") return payload.message;
  if (typeof payload.message?.content === "string")
    return payload.message.content;
  const parts = Array.isArray(payload.parts) ? payload.parts : [];
  return parts
    .map((part) => (typeof part?.text === "string" ? part.text : ""))
    .filter(Boolean)
    .join("\n");
}

function groupMessages(messages, runs) {
  const groups = new Map();
  const runStatuses = new Map(
    runs.map((run) => [run.thread_id + "/" + run.id, run.status]),
  );
  const ordered = [...messages].sort((left, right) =>
    BigInt(left.id) < BigInt(right.id)
      ? -1
      : BigInt(left.id) > BigInt(right.id)
        ? 1
        : 0,
  );
  for (const message of ordered) {
    const key = message.thread_id + "/" + message.run_id;
    if (!groups.has(key))
      groups.set(key, {
        threadID: message.thread_id,
        runID: message.run_id,
        status: runStatuses.get(key) || "",
        messages: [],
      });
    groups.get(key).messages.push(message);
  }
  return [...groups.values()].reverse();
}

if (typeof module !== "undefined")
  module.exports = { getCategory, getMessageText, groupMessages };
if (typeof document !== "undefined") initializeAdmin();

function initializeAdmin() {
  const $ = (id) => document.getElementById(id);
  const english = {
    查询类型: "Query type",
    "查询 ID": "Query ID",
    消息管理: "Message inspector",
    消息查询: "Message search",
    最近查询: "Recent searches",
    "返回 Agent": "Back to Agent",
    只读消息查询: "Read-only message search",
    "任务 ID": "Task ID",
    "执行 ID": "Run ID",
    "消息 ID": "Message ID",
    查询: "Search",
    "输入完整 ID": "Enter the complete ID",
    "支持任务 / 执行 / 消息 ID": "Task, run, or message ID",
    查询任务的完整消息: "Understand every message in a task",
    "从 Agent 打开消息管理，或输入任务、执行、消息 ID。":
      "Open the inspector from Agent, or enter a task, run, or message ID.",
    "查看输入、模型请求、工具调用和执行结果。":
      "Explore inputs, model requests, tool calls and results.",
    任务: "Task",
    打开任务: "Open task",
    包含子任务: "Include subtasks",
    刷新: "Refresh",
    导出已加载消息: "Export loaded messages",
    加载更多: "Load more",
    "模型请求从此功能启用后开始记录，旧记录无法补回。查询不会修改原始数据。":
      "Model requests are recorded from this feature onward. Older requests cannot be recovered. Inspection does not modify data.",
    全部: "All",
    输入: "Inputs",
    模型: "Model",
    工具: "Tools",
    错误: "Errors",
    系统: "System",
    执行: "Run",
    尚未分配执行: "Not assigned to a run",
    用户输入: "User input",
    模型请求: "Model request",
    处理后模型输出: "Processed model response",
    原始模型响应: "Raw model response",
    工具调用: "Tool call",
    审批: "Approval",
    待回复: "Awaiting answer",
    续跑输入: "Resume input",
    计划: "Plan",
    消息: "Message",
    待处理: "Pending",
    已接收: "Accepted",
    已取消: "Cancelled",
    已完成: "Completed",
    失败: "Failed",
    已中断: "Interrupted",
    等待回复: "Waiting for input",
    运行中: "Running",
    请求参数: "Arguments",
    工具返回结果: "Tool result",
    查看原始消息: "View raw message",
    "复制 ID": "Copy ID",
    已复制: "Copied",
    "复制失败，请手动选择 ID": "Copy failed; select the ID manually",
    没有此类型的已加载消息: "No loaded messages of this type",
    该任务还没有消息: "This task has no messages yet",
    尚无最近查询: "No recent searches",
    "加载中…": "Loading…",
    "正在查询…": "Searching…",
    服务已连接: "Service connected",
    连接失败: "Connection failed",
    "无法连接服务，请稍后重试": "Cannot connect to the service. Please retry.",
    未命名任务: "Untitled task",
    "未找到该 ID，请检查查询类型和 ID":
      "ID not found. Check the query type and ID.",
    "请选择查询类型并输入有效 ID；游标不能为负数，每页最多 200 条":
      "Enter a valid ID and query type. The cursor must be nonnegative; at most 200 messages per page.",
    条上下文: "context messages",
    已加载: "Loaded",
    条消息: "messages",
    还有更多消息: "more messages available",
  };
  let language = readStorage("deepagent-admin-language", "zh");
  let recent = readStorage("deepagent-admin-recent", []);
  if (!Array.isArray(recent)) recent = [];
  let result = null;
  let currentQuery = null;
  let filter = "all";
  let queryVersion = 0;
  let pendingRequest = null;
  const translate = (text) =>
    language === "en" ? english[text] || text : text;
  const statusLabels = {
    finished: "已完成",
    failed: "失败",
    interrupted: "已中断",
    blocked: "等待回复",
    started: "运行中",
    pending: "待处理",
    accepted: "已接收",
    canceled: "已取消",
  };
  const kindLabels = {
    input: "用户输入",
    model_request: "模型请求",
    assistant: "处理后模型输出",
    model_response: "原始模型响应",
    tool: "工具调用",
    error: "错误",
    approval: "审批",
    question: "待回复",
    interrupt: "待回复",
    resume_run: "续跑输入",
    plan: "计划",
  };

  function readStorage(key, fallback) {
    try {
      return JSON.parse(localStorage.getItem(key)) ?? fallback;
    } catch {
      return fallback;
    }
  }
  function saveStorage(key, value) {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch {
      /* 禁用存储不影响查询。 */
    }
  }
  function element(tag, className, text) {
    const node = document.createElement(tag);
    node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function showNotice(text, loading = false) {
    $("notice").textContent = translate(text);
    $("notice").className = loading ? "notice loading" : "notice";
    $("notice").hidden = !text;
  }
  function copyButton(value) {
    const button = element("button", "copy-id", value);
    button.type = "button";
    button.dataset.copy = value;
    button.title = translate("复制 ID");
    return button;
  }
  function addJSON(parent, label, value) {
    parent.append(
      element("p", "payload-label", translate(label)),
      element(
        "pre",
        "",
        typeof value === "string" ? value : JSON.stringify(value, null, 2),
      ),
    );
  }
  function renderMessage(message) {
    const category = getCategory(message.kind);
    const row = element(
      "article",
      "message" +
        (result.matched_message_id === message.id ? " is-focused" : ""),
    );
    row.dataset.messageId = message.id;
    row.dataset.kind = message.kind;
    const time = new Date(message.created_at);
    row.append(
      element(
        "time",
        "",
        time.getFullYear() > 1970
          ? time.toLocaleTimeString(language === "zh" ? "zh-CN" : "en-GB", {
              hour12: false,
            })
          : "—",
      ),
    );
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.classList.add("icon");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", "#icon-" + category);
    icon.append(use);
    const iconBox = element("span", "message-icon " + category);
    iconBox.append(icon);
    row.append(iconBox);
    const content = element("div", "message-main");
    const meta = element("div", "message-meta");
    meta.append(
      element(
        "strong",
        "",
        translate(kindLabels[message.kind] || message.kind),
      ),
      copyButton(message.id),
    );
    if (message.status)
      meta.append(
        element(
          "span",
          "badge",
          translate(statusLabels[message.status] || message.status),
        ),
      );
    content.append(meta);
    const payload = message.payload || {};
    const text = getMessageText(message);
    if (text)
      content.append(
        element(
          "div",
          "message-content " + (category === "error" ? "error" : ""),
          text,
        ),
      );
    const details = element("details", "");
    let summary = translate("查看原始消息");
    if (message.kind === "model_request")
      summary = `${payload.messages?.length || 0} ${translate("条上下文")}`;
    if (message.kind === "tool")
      summary = `${payload.tool_name || "tool"} · ${payload.tool_call_id || ""}`;
    details.append(element("summary", "", summary));
    if (message.kind === "tool") {
      details.open = result.matched_message_id === message.id;
      if (payload.arguments_json !== undefined)
        addJSON(details, "请求参数", payload.arguments_json);
      if (payload.result_json !== undefined)
        addJSON(details, "工具返回结果", payload.result_json);
    }
    addJSON(details, "查看原始消息", message.payload);
    content.append(details);
    row.append(content);
    return row;
  }
  function renderResults() {
    if (!result) return;
    $("results").hidden = false;
    $("empty").hidden = true;
    $("taskTitle").textContent = result.thread.untitled
      ? translate("未命名任务")
      : result.thread.title;
    $("copyThread").textContent = result.thread.id;
    $("copyThread").dataset.copy = result.thread.id;
    $("copyThread").title = translate("复制 ID");
    $("openTask").href = "/?thread=" + encodeURIComponent(result.thread.id);
    $("includeSubtasks").disabled = currentQuery.kind !== "thread";
    $("includeSubtasks").checked =
      currentQuery.includeSubtasks && currentQuery.kind === "thread";
    $("filters").replaceChildren();
    for (const [category, label] of Object.entries({
      all: "全部",
      input: "输入",
      model: "模型",
      tool: "工具",
      error: "错误",
      system: "系统",
    })) {
      const count = result.messages.filter(
        (message) =>
          category === "all" || getCategory(message.kind) === category,
      ).length;
      const button = element("button", "", `${translate(label)} ${count}`);
      button.setAttribute("aria-pressed", String(filter === category));
      button.onclick = () => {
        filter = category;
        renderResults();
      };
      $("filters").append(button);
    }
    $("pageStatus").textContent =
      `${translate("已加载")} ${result.messages.length} ${translate("条消息")}${result.has_more ? " · " + translate("还有更多消息") : ""}`;
    const visible = result.messages.filter(
      (message) => filter === "all" || getCategory(message.kind) === filter,
    );
    $("timeline").replaceChildren();
    for (const group of groupMessages(visible, result.runs)) {
      const container = element("details", "run-group");
      container.open = true;
      container.dataset.runId = group.runID;
      const heading = element("summary", "run-heading");
      heading.append(
        element("strong", "", translate(group.runID ? "执行" : "尚未分配执行")),
      );
      if (group.runID) heading.append(element("span", "run-id", group.runID));
      if (group.status)
        heading.append(
          element(
            "span",
            "badge " + group.status,
            translate(statusLabels[group.status] || group.status),
          ),
        );
      if (group.threadID !== result.thread.id) {
        const child = result.threads.find(
          (thread) => thread.id === group.threadID,
        );
        heading.append(
          element("span", "run-thread", child?.title || group.threadID),
        );
      }
      const messages = element("div", "run-messages");
      for (const message of group.messages)
        messages.append(renderMessage(message));
      container.append(heading, messages);
      $("timeline").append(container);
    }
    if (!visible.length)
      $("timeline").append(
        element(
          "p",
          "history-note",
          translate(
            result.messages.length
              ? "没有此类型的已加载消息"
              : "该任务还没有消息",
          ),
        ),
      );
    $("loadMore").hidden = !result.has_more;
    $("export").disabled = !result.messages.length;
  }
  function renderRecent() {
    $("recent").replaceChildren();
    for (const item of recent.slice(0, 10)) {
      if (
        !item ||
        !["thread", "run", "message"].includes(item.kind) ||
        typeof item.id !== "string"
      )
        continue;
      const button = element("button", "recent-item");
      button.append(
        element("strong", "", item.title || item.id),
        element(
          "small",
          "",
          `${translate({ thread: "任务 ID", run: "执行 ID", message: "消息 ID" }[item.kind])} · ${item.id}`,
        ),
      );
      button.setAttribute(
        "aria-current",
        String(
          currentQuery?.id === item.id && currentQuery?.kind === item.kind,
        ),
      );
      button.onclick = () => {
        $("queryKind").value = item.kind;
        $("queryID").value = item.id;
        $("includeSubtasks").checked =
          item.kind === "thread" && Boolean(item.includeSubtasks);
        loadMessages();
      };
      $("recent").append(button);
    }
    if (!$("recent").childElementCount)
      $("recent").append(
        element("p", "sidebar-label", translate("尚无最近查询")),
      );
  }
  async function loadMessages(append = false) {
    const version = ++queryVersion;
    pendingRequest?.abort();
    pendingRequest = new AbortController();
    if (!append) {
      const query = {
        kind: $("queryKind").value,
        id: $("queryID").value.trim(),
        includeSubtasks:
          $("queryKind").value === "thread" && $("includeSubtasks").checked,
      };
      const changed =
        !currentQuery ||
        query.kind !== currentQuery.kind ||
        query.id !== currentQuery.id ||
        query.includeSubtasks !== currentQuery.includeSubtasks;
      currentQuery = query;
      if (changed) {
        result = null;
        filter = "all";
        $("results").hidden = true;
      }
      $("empty").hidden = true;
    }
    const parameters = new URLSearchParams({
      kind: currentQuery.kind,
      id: currentQuery.id,
    });
    if (currentQuery.kind === "thread" && currentQuery.includeSubtasks)
      parameters.set("include_subtasks", "true");
    if (append && result?.next_id) parameters.set("after", result.next_id);
    const shareParameters = new URLSearchParams({
      kind: currentQuery.kind,
      id: currentQuery.id,
    });
    if (currentQuery.kind === "thread" && currentQuery.includeSubtasks)
      shareParameters.set("include_subtasks", "true");
    history.replaceState(null, "", "/admin/?" + shareParameters);
    $("loadMore").disabled = true;
    $("refresh").disabled = true;
    showNotice(append ? "加载中…" : "正在查询…", true);
    try {
      const response = await fetch("/api/admin/messages?" + parameters, {
        signal: pendingRequest.signal,
        cache: "no-store",
      });
      const page = await response.json();
      if (version !== queryVersion) return;
      if (!response.ok)
        throw new Error(page.error || `HTTP ${response.status}`);
      if (append) {
        const byID = new Map(
          result.messages.map((message) => [message.id, message]),
        );
        for (const message of page.messages) byID.set(message.id, message);
        page.messages = [...byID.values()];
        const runs = new Map(
          result.runs.map((run) => [run.thread_id + "/" + run.id, run]),
        );
        for (const run of page.runs)
          runs.set(run.thread_id + "/" + run.id, run);
        page.runs = [...runs.values()];
      }
      result = page;
      $("connection").textContent = translate("服务已连接");
      $("connection").dataset.online = "true";
      showNotice("");
      renderResults();
      const item = { ...currentQuery, title: page.thread.title };
      recent = [
        item,
        ...recent.filter(
          (entry) => entry?.id !== item.id || entry?.kind !== item.kind,
        ),
      ].slice(0, 10);
      saveStorage("deepagent-admin-recent", recent);
      renderRecent();
      if (result.matched_message_id && !append)
        document
          .querySelector(".is-focused")
          ?.scrollIntoView({ block: "center" });
    } catch (error) {
      if (error.name === "AbortError" || version !== queryVersion) return;
      showNotice(
        error instanceof TypeError ? "无法连接服务，请稍后重试" : error.message,
      );
      $("connection").textContent = translate("连接失败");
      $("connection").dataset.online = "false";
    } finally {
      if (version === queryVersion) {
        $("loadMore").disabled = false;
        $("refresh").disabled = false;
      }
    }
  }
  function applyLanguage() {
    document.documentElement.lang = language === "en" ? "en" : "zh-CN";
    for (const node of document.querySelectorAll("[data-i18n]")) {
      node.dataset.originalText ||= node.textContent;
      node.textContent = translate(node.dataset.originalText);
    }
    $("queryID").placeholder = translate("输入完整 ID");
    $("language").textContent = language === "zh" ? "English" : "中文";
    $("queryKind").setAttribute("aria-label", translate("查询类型"));
    $("queryID").setAttribute("aria-label", translate("查询 ID"));
    renderRecent();
    renderResults();
  }
  $("searchForm").onsubmit = (event) => {
    event.preventDefault();
    loadMessages();
  };
  $("refresh").onclick = () => loadMessages();
  $("loadMore").onclick = () => loadMessages(true);
  $("includeSubtasks").onchange = () => loadMessages();
  $("queryKind").onchange = () => {
    $("includeSubtasks").disabled = $("queryKind").value !== "thread";
  };
  $("newQuery").onclick = () => {
    $("queryID").focus();
    $("queryID").select();
  };
  $("language").onclick = () => {
    language = language === "zh" ? "en" : "zh";
    saveStorage("deepagent-admin-language", language);
    applyLanguage();
  };
  $("export").onclick = () => {
    const url = URL.createObjectURL(
      new Blob([JSON.stringify(result, null, 2)], { type: "application/json" }),
    );
    const link = element("a", "");
    link.href = url;
    link.download = `messages-${result.thread.id}.json`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  document.addEventListener("click", async (event) => {
    const button = event.target.closest("[data-copy]");
    if (!button) return;
    try {
      await navigator.clipboard.writeText(button.dataset.copy);
      button.title = translate("已复制");
    } catch {
      showNotice("复制失败，请手动选择 ID");
    }
  });
  const parameters = new URLSearchParams(location.search);
  if (["thread", "run", "message"].includes(parameters.get("kind")))
    $("queryKind").value = parameters.get("kind");
  $("queryID").value = parameters.get("id") || "";
  $("includeSubtasks").checked = parameters.get("include_subtasks") === "true";
  applyLanguage();
  if ($("queryID").value) loadMessages();
}
