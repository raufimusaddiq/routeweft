package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// qoderMaxRequestBytes is the bound on the Chat body QoderDo reads before
// preparing the envelope.
const qoderMaxRequestBytes = 32 << 20

// qoderClientFor builds the Qoder client for one exchange; tests replace it.
var qoderClientFor = func(client *http.Client) *QoderClient { return NewQoderClient(client) }

// QoderDo is QoderExecutor.execute's send half (executors/qoder.js:613-702): the
// Chat body is converted to Qoder's signed envelope, then sent once. A failure
// the pin answers locally is returned as that HTTP status; the caller never
// receives a request signed with credentials the gateway will refuse.
func QoderDo(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, qoderMaxRequestBytes+1))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > qoderMaxRequestBytes {
		return nil, errors.New("qoder request exceeds the limit")
	}
	settings := provider.Connection.Settings
	prepared, err := qoderClientFor(client).PrepareChat(request.Context(), QoderChatInput{
		Model: provider.UpstreamModel,
		Body:  raw,
		Credentials: QoderCredentials{
			AccessToken: provider.APIToken, UserID: settings.UserID, MachineID: settings.MachineID,
			DisplayName: provider.Connection.Name, Email: settings.Email,
		},
	})
	if err != nil {
		var local *QoderLocalError
		if errors.As(err, &local) {
			return qoderRefusal(request, local.Status, local.Message), nil
		}
		return nil, err
	}
	outbound, err := http.NewRequestWithContext(request.Context(), http.MethodPost, prepared.URL, bytes.NewReader(prepared.Body))
	if err != nil {
		return nil, err
	}
	for name, value := range prepared.Headers {
		outbound.Header.Set(name, value)
	}
	return client.Do(outbound)
}

// qoderRefusal is the pin's local answer, in the same shape as its upstream
// error body, so the dispatcher classifies it by status.
func qoderRefusal(request *http.Request, status int, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": strings.TrimSpace(message), "code": status}})
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request,
	}
}
