package policy

import "eino-cli/internal/tools"

type Policy struct{}

func New() *Policy {
	return &Policy{}
}

func (p *Policy) RequiresApproval(tool tools.Tool) bool {
	return tool.RequiresApproval || tool.RiskLevel == tools.RiskLevelHigh
}
