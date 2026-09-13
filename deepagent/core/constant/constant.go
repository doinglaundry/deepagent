package constant

import "context"

const Version = "1.0.0"
const ErrMsgModelRequired = "model is required"

func LookupModelContextWindow(context.Context, string) int { return 128000 }

// Skill activation wording transcribed from the architecture owner's dictation.
const SkillActivatedMessageFormat = "技能%s已激活**重要**：技能目录路径： %s。%s。"

const SkillActivatedSimpleFormat = "技能%s已激活。\n技能目录：%s"
