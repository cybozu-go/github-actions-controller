package agent

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	constants "github.com/cybozu-go/github-actions-controller"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// SocketModeClient is a client for Slack socket mode.
type SocketModeClient struct {
	client *socketmode.Client
}

// NewSocketModeClient creates SocketModeClient.
func NewSocketModeClient(
	appToken string,
	botToken string,
) *SocketModeClient {
	return &SocketModeClient{
		client: socketmode.New(
			slack.New(
				botToken,
				slack.OptionAppLevelToken(appToken),
				slack.OptionDebug(true),
				slack.OptionLog(log.New(os.Stdout, "api: ", log.Lshortfile|log.LstdFlags)),
			),
			socketmode.OptionDebug(true),
			socketmode.OptionLog(log.New(os.Stdout, "socketmode: ", log.Lshortfile|log.LstdFlags)),
		),
	}
}

// Run makes a connectionh with Slack over WebSocket.
func (s *SocketModeClient) Run() error {
	return s.client.Run()
}

// ListenInteractiveEvents listens to events from interactive components and
// runs the event handler.
func (s *SocketModeClient) ListenInteractiveEvents() error {
	for envelope := range s.client.Events {
		if envelope.Type != socketmode.EventTypeInteractive {
			continue
		}
		cb, ok := envelope.Data.(slack.InteractionCallback)
		if !ok {
			return fmt.Errorf(
				"received data cannot be converted into slack.InteractionCallback: %#v",
				envelope.Data,
			)
		}
		if cb.Type != slack.InteractionTypeBlockActions {
			continue
		}

		if envelope.Request == nil {
			return fmt.Errorf("request should not be nil: %#v", envelope.Data)
		}

		name, namespace, err := s.extractNameFromJobResultMsg(&cb)
		if err != nil {
			return err
		}

		var stderr bytes.Buffer
		command := exec.Command(
			"kubectl", "annotate", "pods",
			"-n", namespace, name,
			fmt.Sprintf(
				"%s=%s",
				constants.PodDeletionTimeKey,
				// added duration is now fixed, but can be configurable later.
				time.Now().Add(20*time.Minute).UTC().Format(time.RFC3339),
			),
			"--overwrite",
		)
		command.Stdout = os.Stdout
		command.Stderr = &stderr
		err = command.Run()
		if err != nil {
			return fmt.Errorf(
				"failed to annotate %s in %s with %s: err=%#v, stderr=%s",
				name,
				namespace,
				constants.PodDeletionTimeKey,
				err,
				stderr.String(),
			)
		}

		payload, err := s.makeExtendCallbackPayload(name, namespace), nil
		if err != nil {
			return err
		}

		s.client.Ack(*envelope.Request, payload)
	}
	return nil
}

func (s *SocketModeClient) makeExtendCallbackPayload(
	podNamespace string,
	podName string,
) []slack.Attachment {
	return []slack.Attachment{
		{
			// "warning" is yellow
			Color: "warning",
			Text: fmt.Sprintf(
				"%s in %s is extended successfully",
				podName,
				podNamespace,
			),
		},
	}
}

func (s *SocketModeClient) extractNameFromJobResultMsg(body *slack.InteractionCallback) (string, string, error) {
	if len(body.Message.Attachments) != 1 {
		return "", "", fmt.Errorf(
			"length of attachments should be 1, but got %d: %#v",
			len(body.Message.Attachments),
			body.Message.Attachments,
		)
	}

	var name, namespace string
	a := body.Message.Attachments[0]
	for _, v := range a.Fields {
		switch v.Title {
		case podNameTitle:
			name = v.Value
		case podNamespaceTitle:
			namespace = v.Value
		}
	}

	if len(name) == 0 {
		return "", "", fmt.Errorf(`the field "%s" should not be empty`, podNameTitle)
	}
	if len(namespace) == 0 {
		return "", "", fmt.Errorf(`the field "%s" should not be empty`, podNamespaceTitle)
	}

	return name, namespace, nil
}
