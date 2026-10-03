// Package skills loads configured instructions and project skill catalogs.
package skills

import "context"

type SkillLoader interface {
	ListSkills(context.Context) ([]*SkillMetadata, error)
}
type SkillMetadata struct {
	Name        string
	Description string
	Path        string
}
