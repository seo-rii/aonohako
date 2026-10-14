package runvalidation

import (
	"encoding/base64"
	"fmt"

	"aonohako/internal/entrypolicy"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

func ValidateEntry(req *model.RunRequest) error {
	for _, program := range req.Programs {
		if profiles.NormalizeRunLang(program.Lang) == "entry" {
			return fmt.Errorf("Entry requires a standalone execution")
		}
	}
	if req.Interactor != nil && profiles.NormalizeRunLang(req.Interactor.Lang) == "entry" ||
		req.SPJ != nil && profiles.NormalizeRunLang(req.SPJ.Lang) == "entry" {
		return fmt.Errorf("Entry cannot be used as a judge or interactor")
	}
	if profiles.NormalizeRunLang(req.Lang) != "entry" {
		if req.EntryMode != "" {
			return fmt.Errorf("entry_mode requires entry execution")
		}
		return nil
	}
	if req.EntryMode != "stdio" && req.EntryMode != "state" {
		return fmt.Errorf("entry_mode must be stdio or state")
	}
	if req.Pipeline != nil || req.Communication != nil || req.Interactor != nil || req.SPJ != nil || len(req.Programs) != 0 || len(req.Steps) != 0 ||
		len(req.FileOutputs) != 0 || len(req.SidecarOutputs) != 0 || req.EnableNetwork || req.IgnoreTLE {
		return fmt.Errorf("Entry requires isolated standalone execution without custom judges or file outputs")
	}
	if len(req.Binaries) != 1 || req.Binaries[0].Name != entrypolicy.SourceName || req.Binaries[0].Mode != "" ||
		(req.EntryPoint != "" && req.EntryPoint != entrypolicy.SourceName) {
		return fmt.Errorf("Entry requires only Main.entry.json as data")
	}
	if len(req.Binaries[0].DataB64) > base64.StdEncoding.EncodedLen(entrypolicy.MaxSubmissionBytes) {
		return fmt.Errorf("Entry submission is too large")
	}
	if len(req.Stdin) > entrypolicy.MaxInputBytes {
		return fmt.Errorf("Entry input is too large")
	}
	if req.EntryMode == "state" && req.ExpectedStdoutURL == "" {
		if err := entrypolicy.ValidateExpected([]byte(req.ExpectedStdout)); err != nil {
			return err
		}
	}
	return nil
}
