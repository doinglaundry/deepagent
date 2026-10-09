package model

import (
	"context"
)

type SkillLoader interface {
	ListSkills(context.Context) ([]*SkillMetadata, error)
}

type SkillMetadata struct {
	Name        string
	Description string
	Path        string
}
