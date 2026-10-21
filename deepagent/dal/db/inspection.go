package db

import (
	"context"
	"errors"
	"strconv"

	agentmodel "eino-cli/deepagent/model"
)

// InspectMessages 只读现有表。消息和执行查询定位到所属任务，不创建新的业务状态。
func (dao *MessageDAO) InspectMessages(ctx context.Context, query agentmodel.MessageInspectionQuery) (*agentmodel.MessageInspection, error) {
	err := query.Validate()
	if err != nil {
		return nil, err
	}
	connection := dao.Client.DB(ctx, true)
	threadID, _ := strconv.ParseInt(query.ID, 10, 64)
	result := &agentmodel.MessageInspection{}
	switch query.Kind {
	case "run":
		var run agentmodel.RunRecord
		err = connection.Where("run_id = ?", query.ID).Take(&run).Error
		threadID = run.ThreadID
	case "message":
		var message agentmodel.MailboxMessage
		err = connection.Where("message_id = ?", threadID).Take(&message).Error
		threadID = message.ThreadID
		result.MatchedMessageID = strconv.FormatInt(message.MessageID, 10)
	}
	if err != nil {
		return nil, err
	}
	result.Thread = &agentmodel.ThreadRecord{}
	err = connection.Where("thread_id = ?", threadID).Take(result.Thread).Error
	if err != nil {
		return nil, err
	}
	threadIDs := []int64{threadID}
	// UNION DISTINCT 避免异常父子闭环；查询超时由 HTTP context 限制。
	if query.Kind == "thread" && query.IncludeSubtasks {
		err = connection.Raw(`WITH RECURSIVE task_tree(thread_id) AS (
 SELECT thread_id FROM thread WHERE thread_id = ?
 UNION DISTINCT
 SELECT child.thread_id FROM thread child JOIN task_tree parent
 ON JSON_UNQUOTE(JSON_EXTRACT(child.metadata_json,'$.parent_thread_id')) = CAST(parent.thread_id AS CHAR)
) SELECT thread_id FROM task_tree LIMIT 1001`, threadID).Scan(&threadIDs).Error
		if err != nil {
			return nil, err
		}
		if len(threadIDs) > 1000 {
			return nil, errors.New("子任务超过 1000 个，请按执行或消息 ID 缩小查询范围")
		}
	}
	err = connection.Where("thread_id IN ?", threadIDs).Order("thread_id ASC").Find(&result.Threads).Error
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit == 0 {
		limit = 100
	}
	// 消息定位从焦点前最多半页开始，避免长任务的首屏找不到被查询的消息。
	if query.Kind == "message" && query.AfterID == 0 {
		focalID, _ := strconv.ParseInt(query.ID, 10, 64)
		var precedingIDs []int64
		err = connection.Model(&agentmodel.MailboxMessage{}).Where("thread_id = ? AND message_id < ?", threadID, focalID).Order("message_id DESC").Limit(limit/2).Pluck("message_id", &precedingIDs).Error
		if err != nil {
			return nil, err
		}
		query.AfterID = focalID - 1
		if len(precedingIDs) > 0 {
			query.AfterID = precedingIDs[len(precedingIDs)-1] - 1
		}
	}
	messageQuery := connection.Where("thread_id IN ? AND message_id > ?", threadIDs, query.AfterID)
	if query.Kind == "run" {
		messageQuery = messageQuery.Where("trigger_turn_id = ?", query.ID)
	}
	err = messageQuery.Order("message_id ASC").Limit(limit + 1).Find(&result.Messages).Error
	if err != nil {
		return nil, err
	}
	result.HasMore = len(result.Messages) > limit
	if result.HasMore {
		result.Messages = result.Messages[:limit]
	}
	runIDs := make([]string, 0, len(result.Messages)+1)
	for _, message := range result.Messages {
		runIDs = append(runIDs, message.TriggerRunID)
	}
	for _, thread := range result.Threads {
		runIDs = append(runIDs, thread.LastRunID)
	}
	if query.Kind == "run" {
		runIDs = append(runIDs, query.ID)
	}
	err = connection.Where("thread_id IN ? AND run_id IN ?", threadIDs, runIDs).Find(&result.Runs).Error
	return result, err
}
