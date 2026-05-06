package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type TeamsForwarder struct {
	tenantID        string
	clientID        string
	clientSecret    string
	fallbackChannel string // webhook URL for fallback channel posting
}

func NewTeamsForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*TeamsForwarder, error) {
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("teams forwarder requires secretRef")
	}

	namespace := "ovro-system"
	secretData, err := secretGetter.GetSecretData(ctx, cfg.SecretRef, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	tenantID, _ := secretData["tenantId"]
	clientID, _ := secretData["clientId"]
	clientSecret, _ := secretData["clientSecret"]

	if len(tenantID) == 0 || len(clientID) == 0 || len(clientSecret) == 0 {
		return nil, fmt.Errorf("secret must contain tenantId, clientId, and clientSecret")
	}

	return &TeamsForwarder{
		tenantID:        string(tenantID),
		clientID:        string(clientID),
		clientSecret:    string(clientSecret),
		fallbackChannel: cfg.Channel,
	}, nil
}

func (t *TeamsForwarder) Name() string {
	return "teams"
}

func (t *TeamsForwarder) Send(ctx context.Context, n *Notification) error {
	token, err := t.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("getting access token: %w", err)
	}

	owner := n.Owner

	// If owner is an email, strip domain and try as UPN first
	if strings.Contains(owner, "@") {
		userID, err := t.lookupUser(ctx, token, owner)
		if err != nil {
			// Try just the username part
			username := owner[:strings.Index(owner, "@")]
			userID, err = t.lookupUser(ctx, token, username)
			if err != nil {
				if t.fallbackChannel != "" {
					return t.sendViaWebhook(ctx, n)
				}
				return fmt.Errorf("could not find Teams user %q and no fallback channel configured: %w", owner, err)
			}
		}
		return t.sendChatMessage(ctx, token, userID, n)
	}

	// If owner starts with # treat as channel — use webhook fallback
	if strings.HasPrefix(owner, "#") {
		if t.fallbackChannel != "" {
			return t.sendViaWebhook(ctx, n)
		}
		return fmt.Errorf("channel routing requires a webhook URL in the channel config field")
	}

	// Try as a user ID or UPN directly
	userID, err := t.lookupUser(ctx, token, owner)
	if err != nil {
		if t.fallbackChannel != "" {
			return t.sendViaWebhook(ctx, n)
		}
		return fmt.Errorf("could not resolve Teams user %q: %w", owner, err)
	}
	return t.sendChatMessage(ctx, token, userID, n)
}

func (t *TeamsForwarder) getAccessToken(ctx context.Context) (string, error) {
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", t.tenantID)

	data := fmt.Sprintf("grant_type=client_credentials&client_id=%s&client_secret=%s&scope=https://graph.microsoft.com/.default",
		t.clientID, t.clientSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("failed to get token: %s", result.Error)
	}
	return result.AccessToken, nil
}

func (t *TeamsForwarder) lookupUser(ctx context.Context, token, identifier string) (string, error) {
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s", identifier)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("user lookup returned %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("user not found")
	}
	return result.ID, nil
}

func (t *TeamsForwarder) sendChatMessage(ctx context.Context, token, userID string, n *Notification) error {
	// Create a 1:1 chat with the user
	chatPayload := map[string]interface{}{
		"chatType": "oneOnOne",
		"members": []map[string]interface{}{
			{
				"@odata.type":    "#microsoft.graph.aadUserConversationMember",
				"roles":          []string{"owner"},
				"user@odata.bind": fmt.Sprintf("https://graph.microsoft.com/v1.0/users('%s')", userID),
			},
		},
	}

	chatJSON, _ := json.Marshal(chatPayload)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://graph.microsoft.com/v1.0/chats", bytes.NewBuffer(chatJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating chat: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var chatResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &chatResult); err != nil {
		return err
	}
	if chatResult.ID == "" {
		return fmt.Errorf("failed to create chat, response: %s", string(body))
	}

	// Send message to the chat
	message := fmt.Sprintf(
		"<h2>VM Rightsizing Recommendation</h2>"+
			"<b>VM:</b> %s<br/>"+
			"<b>Namespace:</b> %s<br/>"+
			"<b>Owner:</b> %s<br/>"+
			"<b>Direction:</b> %s<br/>"+
			"<b>Current:</b> %d CPU, %d GiB Memory<br/>"+
			"<b>Recommended:</b> %d CPU, %d GiB Memory<br/><br/>"+
			"<a href=\"%s\">Approve/Reject</a>",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	msgPayload := map[string]interface{}{
		"body": map[string]string{
			"contentType": "html",
			"content":     message,
		},
	}

	msgJSON, _ := json.Marshal(msgPayload)
	msgURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/chats/%s/messages", chatResult.ID)
	msgReq, err := http.NewRequestWithContext(ctx, "POST", msgURL, bytes.NewBuffer(msgJSON))
	if err != nil {
		return err
	}
	msgReq.Header.Set("Authorization", "Bearer "+token)
	msgReq.Header.Set("Content-Type", "application/json")

	msgResp, err := http.DefaultClient.Do(msgReq)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	defer msgResp.Body.Close()

	if msgResp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(msgResp.Body)
		return fmt.Errorf("send message returned %d: %s", msgResp.StatusCode, string(respBody))
	}

	return nil
}

// sendViaWebhook posts to a Teams channel using an incoming webhook URL as fallback.
func (t *TeamsForwarder) sendViaWebhook(ctx context.Context, n *Notification) error {
	card := map[string]interface{}{
		"type": "message",
		"attachments": []map[string]interface{}{
			{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]interface{}{
					"type":    "AdaptiveCard",
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"version": "1.2",
					"body": []map[string]interface{}{
						{
							"type":   "TextBlock",
							"text":   "VM Rightsizing Recommendation",
							"weight": "Bolder",
							"size":   "Medium",
						},
						{
							"type": "FactSet",
							"facts": []map[string]string{
								{"title": "VM", "value": n.VMName},
								{"title": "Namespace", "value": n.Namespace},
								{"title": "Owner", "value": n.Owner},
								{"title": "Direction", "value": n.Direction},
								{"title": "Current CPU", "value": fmt.Sprintf("%d", n.CurrentCPU)},
								{"title": "Current Memory", "value": fmt.Sprintf("%d GiB", n.CurrentMemory)},
								{"title": "Recommended CPU", "value": fmt.Sprintf("%d", n.RecCPU)},
								{"title": "Recommended Memory", "value": fmt.Sprintf("%d GiB", n.RecMemory)},
							},
						},
					},
					"actions": []map[string]interface{}{
						{
							"type":  "Action.OpenUrl",
							"title": "Approve/Reject",
							"url":   n.ApprovalURL,
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.fallbackChannel, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
