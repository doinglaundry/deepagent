package model

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidInspectionQuery = errors.New("请选择查询类型并输入有效 ID；游标不能为负数，每页最多 200 条")

type MessageInspectionQuery struct {
	Kind            string
	ID              string
	IncludeSubtasks bool
	AfterID         int64
	Limit           int
}

func (query MessageInspectionQuery) Validate() error {
	if query.AfterID < 0 || query.Limit < 0 || query.Limit > 200 {
		return ErrInvalidInspectionQuery
	}
	switch query.Kind {
	case "thread", "message":
		id, err := strconv.ParseInt(query.ID, 10, 64)
		if err != nil || id <= 0 {
			return ErrInvalidInspectionQuery
		}
	case "run":
		if strings.TrimSpace(query.ID) == "" || len(query.ID) > 191 {
			return ErrInvalidInspectionQuery
		}
	default:
		return ErrInvalidInspectionQuery
	}
	return nil
}

// 只读查询结果沿用持久化对象；HTTP 边界再把 int64 ID 转成字符串。
type MessageInspection struct {
	Thread           *ThreadRecord
	Threads          []*ThreadRecord
	Runs             []*RunRecord
	Messages         []*MailboxMessage
	HasMore          bool
	MatchedMessageID string
}
