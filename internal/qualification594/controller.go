package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mlnomadpy/dacli/internal/application"
)

func controllerStart(manifestPath string) error {
	m, _, err := loadOwnerManifest(manifestPath)
	if err != nil {
		return err
	}
	p, err := application.OpenNativeOrchestrationProfile(m.Connections["controller"], m.Profiles["controller"])
	if err != nil {
		return err
	}
	status, err := p.Start("start-"+m.Invocation, m.Invocation, "checkpoint-594")
	if err != nil {
		return err
	}
	return save(manifestPath+".controller.json", status)
}
func controllerStatus(manifestPath string) error {
	m, _, err := loadOwnerManifest(manifestPath)
	if err != nil {
		return err
	}
	p, err := application.OpenNativeOrchestrationProfile(m.Connections["controller"], m.Profiles["controller"])
	if err != nil {
		return err
	}
	status, err := p.Status(m.Project, m.Invocation)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(b))
	return nil
}
