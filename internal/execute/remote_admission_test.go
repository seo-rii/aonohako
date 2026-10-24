package execute

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestRemoteRunnerRejectsInvalidPayloadContractBeforeUpload(t *testing.T) {
	for _, req := range []*model.RunRequest{
		{ExpectedStdout: string([]byte{0xe9, '\n'})},
		{SidecarOutputs: []model.OutputFile{{Path: "x"}, {Path: "./x"}}},
	} {
		called := false
		runner := &remoteRunner{
			client: &http.Client{Transport: executeRoundTripFunc(func(*http.Request) (*http.Response, error) {
				called = true
				return nil, fmt.Errorf("unexpected upload")
			})},
			executeURL: "http://remote.example/execute",
		}
		response := runner.Run(context.Background(), req, Hooks{})
		if called || response.Status != model.RunStatusInitFail || (!strings.Contains(response.Reason, "UTF-8") && !strings.Contains(response.Reason, "duplicate sidecar")) {
			t.Fatalf("contract rejection: upload=%v, response=%+v", called, response)
		}
	}
}
