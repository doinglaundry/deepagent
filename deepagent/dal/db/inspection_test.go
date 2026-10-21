package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	agentmodel "eino-cli/deepagent/model"
)

func TestInspectMessagesScopesSubtasksAndPaginates(t *testing.T) {
	dsn := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DEEPAGENT_TEST_MYSQL_DSN for real MySQL inspection tests")
	}
	ctx := context.Background()
	client, err := NewSQL(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.DB(ctx, true).DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	err = MigrateMailbox(ctx, client.DB(ctx, true))
	if err != nil {
		t.Fatal(err)
	}
	baseID := time.Now().UnixNano()
	rootID, childID, unrelatedID := baseID, baseID+1, baseID+2
	threads := []*agentmodel.ThreadRecord{
		{ThreadID: rootID, SessionID: "inspection-test", Status: "open", Metadata: map[string]string{"title": "查询测试"}},
		{ThreadID: childID, SessionID: "inspection-test", Status: "closed", Metadata: map[string]string{"parent_thread_id": strconv.FormatInt(rootID, 10)}},
		{ThreadID: unrelatedID, SessionID: "inspection-test", Status: "open"},
	}
	connectionDB := client.DB(ctx, true)
	err = connectionDB.Create(&threads).Error
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ids := []int64{rootID, childID, unrelatedID}
		connectionDB.Where("thread_id IN ?", ids).Delete(&agentmodel.MailboxMessage{})
		connectionDB.Where("thread_id IN ?", ids).Delete(&agentmodel.RunRecord{})
		connectionDB.Where("thread_id IN ?", ids).Delete(&agentmodel.ThreadRecord{})
	})
	runID := fmt.Sprintf("inspection-%d", baseID)
	err = connectionDB.Create(&agentmodel.RunRecord{RunID: runID, ThreadID: rootID, Status: "finished"}).Error
	if err != nil {
		t.Fatal(err)
	}
	var messages []*agentmodel.MailboxMessage
	for index := range 250 {
		messages = append(messages, &agentmodel.MailboxMessage{MessageID: baseID + 100 + int64(index), ThreadID: rootID, TriggerRunID: runID, MessageType: "assistant", Payload: []byte(`{"parts":[{"type":"text","text":"内容"}]}`), CreatedAt: time.Now()})
	}
	messages = append(messages,
		&agentmodel.MailboxMessage{MessageID: baseID + 400, ThreadID: rootID, MessageType: "input", Status: "pending", Payload: []byte(`{"parts":[{"type":"text","text":"尚未消费"}]}`)},
		&agentmodel.MailboxMessage{MessageID: baseID + 401, ThreadID: childID, MessageType: "assistant", Payload: []byte(`{}`)},
		&agentmodel.MailboxMessage{MessageID: baseID + 402, ThreadID: unrelatedID, MessageType: "assistant", Payload: []byte(`{}`)},
	)
	err = connectionDB.Create(&messages).Error
	if err != nil {
		t.Fatal(err)
	}
	dao := MessageDAO{Client: client}
	query := agentmodel.MessageInspectionQuery{Kind: "thread", ID: strconv.FormatInt(rootID, 10), Limit: 200}
	first, err := dao.InspectMessages(ctx, query)
	if err != nil || len(first.Messages) != 200 || !first.HasMore {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	query.AfterID = first.Messages[199].MessageID
	second, err := dao.InspectMessages(ctx, query)
	if err != nil || len(second.Messages) != 51 || second.HasMore || second.Messages[50].TriggerRunID != "" {
		t.Fatalf("pending input/page lost: %+v err=%v", second, err)
	}
	query.AfterID, query.IncludeSubtasks = 0, true
	allIDs := map[int64]bool{}
	for {
		page, queryErr := dao.InspectMessages(ctx, query)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		for _, message := range page.Messages {
			allIDs[message.ThreadID] = true
		}
		if !page.HasMore {
			break
		}
		query.AfterID = page.Messages[len(page.Messages)-1].MessageID
	}
	if len(allIDs) != 2 || !allIDs[rootID] || !allIDs[childID] {
		t.Fatalf("subtask scope leaked: %+v", allIDs)
	}
	query = agentmodel.MessageInspectionQuery{Kind: "run", ID: runID, Limit: 200}
	result, err := dao.InspectMessages(ctx, query)
	if err != nil || result.Thread.ThreadID != rootID || len(result.Messages) != 200 {
		t.Fatalf("run lookup=%+v err=%v", result, err)
	}
	query = agentmodel.MessageInspectionQuery{Kind: "message", ID: strconv.FormatInt(baseID+345, 10), Limit: 50}
	result, err = dao.InspectMessages(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range result.Messages {
		found = found || message.MessageID == baseID+345
	}
	if !found || result.MatchedMessageID != query.ID {
		t.Fatalf("focal message not in page: %+v", result)
	}
	query.ID = "000" + query.ID
	canonical, err := dao.InspectMessages(ctx, query)
	if err != nil || canonical.MatchedMessageID != strconv.FormatInt(baseID+345, 10) {
		t.Fatalf("noncanonical focus=%+v err=%v", canonical, err)
	}
	_, err = dao.InspectMessages(ctx, agentmodel.MessageInspectionQuery{Kind: "message", ID: strconv.FormatInt(baseID+999, 10)})
	if !errors.Is(err, MySQLErrRecordNotFound) {
		t.Fatalf("missing record: %v", err)
	}
}
