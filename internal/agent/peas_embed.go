package agent

import (
	"embed"
	"fmt"
	"os"
)

//go:embed peas/*
var embeddedPEAS embed.FS

func writeEmbeddedPEASTool(plan *peasExecutionPlan, dst string) (bool, error) {
	data, err := embeddedPEAS.ReadFile("peas/" + plan.filename)
	if err != nil {
		return false, nil
	}
	if len(data) == 0 {
		return false, fmt.Errorf("embedded %s is empty", plan.filename)
	}
	if len(data) > maxPEASToolBytes {
		return false, fmt.Errorf("embedded %s exceeds limit of %d bytes", plan.filename, maxPEASToolBytes)
	}
	if err := os.WriteFile(dst, data, 0600); err != nil {
		return false, fmt.Errorf("write embedded PEAS tool: %w", err)
	}
	return true, nil
}
