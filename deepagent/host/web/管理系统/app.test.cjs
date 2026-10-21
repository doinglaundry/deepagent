const test = require("node:test");
const assert = require("node:assert/strict");
const { groupMessages, getCategory, getMessageText } = require("./app.js");

test("execution groups preserve large IDs, pending inputs and call IDs reused in other runs", () => {
  const messages = [
    {
      id: "9007199254740993",
      thread_id: "100",
      run_id: "first",
      kind: "tool",
      payload: { tool_call_id: "same", result_json: "first" },
    },
    {
      id: "9007199254740992",
      thread_id: "100",
      run_id: "first",
      kind: "input",
      payload: {},
    },
    {
      id: "9007199254740994",
      thread_id: "100",
      run_id: "second",
      kind: "tool",
      payload: { tool_call_id: "same", result_json: "second" },
    },
    {
      id: "9007199254740995",
      thread_id: "100",
      run_id: "",
      kind: "input",
      payload: {},
    },
    {
      id: "9007199254740996",
      thread_id: "101",
      run_id: "second",
      kind: "tool",
      payload: { tool_call_id: "same" },
    },
  ];
  const groups = groupMessages(messages, [
    { id: "first", thread_id: "100", status: "finished" },
  ]);
  assert.equal(groups.length, 4);
  assert.equal(groups[0].threadID, "101");
  const first = groups.find((group) => group.runID === "first");
  assert.deepEqual(
    first.messages.map((message) => message.id),
    ["9007199254740992", "9007199254740993"],
  );
  assert.equal(first.status, "finished");
  assert.equal(first.messages[1].payload.result_json, "first");
  assert.equal(
    groups.find((group) => !group.runID).messages[0].id,
    "9007199254740995",
  );
  assert.equal(messages[0].id, "9007199254740993");
});

test("message categories retain approvals and control inputs rather than dropping unknown kinds", () => {
  assert.equal(getCategory("model_request"), "model");
  assert.equal(getCategory("assistant"), "model");
  assert.equal(getCategory("control.cancel_input"), "input");
  assert.equal(getCategory("resume_run"), "input");
  assert.equal(getCategory("tool"), "tool");
  assert.equal(getCategory("approval"), "system");
  assert.equal(getCategory("error"), "error");
});

test("message text supports old payloads, full responses and malformed stored content", () => {
  assert.equal(
    getMessageText({
      payload: {
        parts: [{ text: "你好" }, { text: "<script>literal</script>" }],
      },
    }),
    "你好\n<script>literal</script>",
  );
  assert.equal(
    getMessageText({ payload: { message: { content: "完整输出" } } }),
    "完整输出",
  );
  assert.equal(
    getMessageText({ payload: { message: "模型请求失败" } }),
    "模型请求失败",
  );
  assert.equal(getMessageText({ payload: "invalid JSON" }), "invalid JSON");
  assert.equal(getMessageText({ payload: null }), "");
});

test("unusual parts remain inspectable without breaking the whole list", () => {
  for (const parts of [
    {},
    "text",
    [null, {}, { text: 123 }, { text: "valid" }],
  ]) {
    assert.equal(
      getMessageText({ payload: { parts } }),
      Array.isArray(parts) ? "valid" : "",
    );
  }
});
