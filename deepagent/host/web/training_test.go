package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	daldb "eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"
)

func TestTrainingMessageSelection(t *testing.T) {
	dsn := os.Getenv("DEEPAGENT_LOCAL_MODEL_TEST_DSN")
	if dsn == "" {
		t.Skip("set dedicated DEEPAGENT_LOCAL_MODEL_TEST_DSN")
	}
	ctx := context.Background()
	client, err := daldb.NewSQL(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	err = daldb.MigrateMailbox(ctx, client.DB(ctx, true))
	if err != nil {
		t.Fatal(err)
	}
	dao := daldb.NewLocalModelDAO(client, fmt.Sprint("selection-", time.Now().UnixNano()))
	err = dao.MigrateSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const threadID int64 = 9007199254740993
	const firstQuestionID int64 = 9007199254741000
	// 固定 ID 验证超过 JavaScript 安全整数范围的消息；只清理本测试的记录。
	cleanup := func() {
		client.DB(ctx, true).Where("thread_id IN ?", []int64{threadID, threadID + 1}).Delete(&agentmodel.MailboxMessage{})
		client.DB(ctx, true).Where("thread_id IN ?", []int64{threadID, threadID + 1}).Delete(&agentmodel.RunRecord{})
	}
	cleanup()
	t.Cleanup(cleanup)
	seed := func(id, thread int64, run, kind, payload string) {
		row := agentmodel.MailboxMessage{MessageID: id, ThreadID: thread, TriggerRunID: run, MessageType: kind, Payload: []byte(payload), Status: "accepted"}
		if kind == "assistant" {
			key := fmt.Sprint(id)
			row.OutputKey = &key
		}
		writeErr := client.DB(ctx, true).Create(&row).Error
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	for index := 0; index < 3; index++ {
		run := fmt.Sprint("selection-run-", index)
		owner := threadID
		if index == 2 {
			owner++
		}
		err = client.DB(ctx, true).Create(&agentmodel.RunRecord{RunID: run, ThreadID: owner, Status: "finished"}).Error
		if err != nil {
			t.Fatal(err)
		}
		question := "Use Chinese comments"
		answer := "I will"
		if index == 1 {
			question, answer = "And variable names?", "Use readable names"
		}
		inputID := firstQuestionID + int64(index)*2
		questionJSON, _ := json.Marshal(agentmodel.UserMessage{Parts: []agentmodel.InputMessagePart{{Type: "text", Text: question}}})
		answerJSON, _ := json.Marshal(agentmodel.MessageEventPayload{Parts: []agentmodel.OutputMessagePart{{Type: "text", Text: answer}}, ConsumedMessageIDs: []string{strconv.FormatInt(inputID, 10)}})
		seed(inputID, owner, run, "input", string(questionJSON))
		seed(inputID+1, owner, run, "assistant", string(answerJSON))
	}
	server := New(nil, t.TempDir())
	server.EnableLocalModel(dao, false)
	call := func(method, path, body string, expected int) []byte {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		server.Handler().ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("%s %s: status=%d body=%s", method, path, response.Code, response.Body.String())
		}
		return response.Body.Bytes()
	}
	sourceID := strconv.FormatInt(firstQuestionID+1, 10)
	path := "/api/local-model/examples?thread_id=" + strconv.FormatInt(threadID, 10) + "&message_id=" + sourceID
	preview := call(http.MethodGet, path, "", http.StatusOK)
	var messages []*agentmodel.Message
	err = json.Unmarshal(preview, &messages)
	if err != nil || len(messages) != 2 || messages[0].Content != "Use Chinese comments" {
		t.Fatalf("preview=%s err=%v", preview, err)
	}
	secondPath := "/api/local-model/examples?thread_id=" + strconv.FormatInt(threadID, 10) + "&message_id=" + strconv.FormatInt(firstQuestionID+3, 10)
	preview = call(http.MethodGet, secondPath+"&include_previous=true", "", http.StatusOK)
	err = json.Unmarshal(preview, &messages)
	if err != nil || len(messages) != 4 || messages[2].Content != "And variable names?" {
		t.Fatalf("context=%s err=%v", preview, err)
	}
	confirmation := fmt.Sprintf(`{"thread_id":"%d","message_id":"%s","confirmed":true,"answer":"中文注释"}`, threadID, sourceID)
	call(http.MethodPost, "/api/local-model/examples", confirmation, http.StatusCreated)
	call(http.MethodPost, "/api/local-model/examples", confirmation, http.StatusCreated)
	status := call(http.MethodGet, "/api/local-model/status", "", http.StatusOK)
	if !strings.Contains(string(status), `"new_example_count":1`) {
		t.Fatalf("duplicate confirmation changed count: %s", status)
	}
	list := call(http.MethodGet, "/api/local-model/examples", "", http.StatusOK)
	var examples []agentmodel.TrainingExample
	err = json.Unmarshal(list, &examples)
	if err != nil || len(examples) != 1 || examples[0].Messages[1].Content != "中文注释" {
		t.Fatalf("examples=%s err=%v", list, err)
	}
	preview = call(http.MethodGet, path, "", http.StatusOK)
	if !strings.Contains(string(preview), "I will") {
		t.Fatal("training edit changed original answer")
	}
	duplicate := fmt.Sprintf(`{"thread_id":"%d","message_id":"%d","confirmed":true,"answer":"中文注释"}`, threadID+1, firstQuestionID+5)
	call(http.MethodPost, "/api/local-model/examples", duplicate, http.StatusCreated)
	call(http.MethodDelete, path, "", http.StatusOK)
	list = call(http.MethodGet, "/api/local-model/examples", "", http.StatusOK)
	err = json.Unmarshal(list, &examples)
	if err != nil || len(examples) != 1 {
		t.Fatalf("removing one source deleted another: %s", list)
	}
	duplicatePath := fmt.Sprintf("/api/local-model/examples?thread_id=%d&message_id=%d", threadID+1, firstQuestionID+5)
	call(http.MethodDelete, duplicatePath, "", http.StatusOK)
	list = call(http.MethodGet, "/api/local-model/examples", "", http.StatusOK)
	err = json.Unmarshal(list, &examples)
	if err != nil || len(examples) != 0 {
		t.Fatalf("last source not removed: %s", list)
	}
	// 拒绝跨任务、未完成执行和图片；不允许把任意页面文本伪造成训练来源。
	wrongThread := fmt.Sprintf("/api/local-model/examples?thread_id=%d&message_id=%s", threadID+1, sourceID)
	call(http.MethodGet, wrongThread, "", http.StatusBadRequest)
	err = client.DB(ctx, true).Model(&agentmodel.RunRecord{}).Where("run_id = ?", "selection-run-0").Update("status", "failed").Error
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodGet, path, "", http.StatusBadRequest)
	err = client.DB(ctx, true).Model(&agentmodel.RunRecord{}).Where("run_id = ?", "selection-run-0").Update("status", "finished").Error
	if err != nil {
		t.Fatal(err)
	}
	err = client.DB(ctx, true).Model(&agentmodel.MailboxMessage{}).Where("message_id = ?", firstQuestionID).Update("payload", []byte(`{"parts":[{"type":"image","url":"https://example.com/image.png"}]}`)).Error
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodGet, path, "", http.StatusBadRequest)
	call(http.MethodGet, path+"0", "", http.StatusBadRequest)
	call(http.MethodPost, "/api/local-model/examples", `{"confirmed":true,"messages":[{"role":"user","content":"forged"}]}`, http.StatusBadRequest)
}
