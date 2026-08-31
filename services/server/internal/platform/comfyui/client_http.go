package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const (
	maxJSONResponseBytes  int64 = 32 << 20
	maxErrorResponseBytes int64 = 64 << 10
	maxUploadImageBytes   int64 = 64 << 20
	maxPromptJSONBytes          = 32 << 20
	maxIdentifierBytes          = 4 << 10
)

type httpClient struct {
	baseURL string
	client  *http.Client
}

// NewClient creates a ComfyUI client using a loopback-only transport.
func NewClient(baseURL string) (Client, error) {
	origin, err := validateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialLoopback
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &httpClient{baseURL: origin, client: client}, nil
}

func validateBaseURL(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", ErrInvalidBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.Host == "" {
		return "", ErrInvalidBaseURL
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", ErrInvalidBaseURL
	}
	if !isAllowedLoopbackHost(parsed.Hostname()) {
		return "", ErrInvalidBaseURL
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil || portText == "" || (len(portText) > 1 && portText[0] == '0') {
		return "", ErrInvalidBaseURL
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", ErrInvalidBaseURL
	}
	if strings.EqualFold(host, "localhost") {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func isAllowedLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return host == "127.0.0.1" || host == "::1"
}

func dialLoopback(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !isAllowedLoopbackHost(host) {
		return nil, ErrInvalidBaseURL
	}
	dialer := &net.Dialer{}
	if !strings.EqualFold(host, "localhost") {
		return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
	}

	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, "localhost")
	if err != nil {
		return nil, errors.New("comfyui localhost resolution failed")
	}
	var lastErr error
	for _, address := range addresses {
		literal := address.IP.String()
		if literal != "127.0.0.1" && literal != "::1" {
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(literal, port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if lastErr != nil {
		return nil, errors.New("comfyui loopback connection failed")
	}
	return nil, errors.New("comfyui localhost did not resolve to an allowed loopback address")
}

func (client *httpClient) SystemStats(ctx context.Context) (SystemStats, error) {
	var result SystemStats
	err := client.getJSON(ctx, "/system_stats", "system stats", &result)
	return result, err
}

func (client *httpClient) ObjectInfo(ctx context.Context) (ObjectInfo, error) {
	var result ObjectInfo
	err := client.getJSON(ctx, "/object_info", "object info", &result)
	return result, err
}

func (client *httpClient) Queue(ctx context.Context) (QueueState, error) {
	var result QueueState
	err := client.getJSON(ctx, "/queue", "queue", &result)
	return result, err
}

func (client *httpClient) UploadImage(ctx context.Context, input UploadImageRequest) (UploadedImage, error) {
	var result UploadedImage
	if input.Content == nil {
		return result, errors.New("comfyui upload content is required")
	}
	if err := requireContext(ctx); err != nil {
		_ = input.Content.Close()
		return result, err
	}
	if strings.TrimSpace(input.Filename) == "" || len(input.Filename) > maxIdentifierBytes || strings.ContainsRune(input.Filename, 0) {
		_ = input.Content.Close()
		return result, errors.New("comfyui upload filename is invalid")
	}
	if input.Size > maxUploadImageBytes {
		_ = input.Content.Close()
		return result, ErrUploadTooLarge
	}
	if len(input.Type) > maxIdentifierBytes || len(input.Subfolder) > maxIdentifierBytes {
		_ = input.Content.Close()
		return result, errors.New("comfyui upload metadata is invalid")
	}

	pipeReader, pipeWriter := io.Pipe()
	var cleanupOnce sync.Once
	cleanup := func(cause error) {
		cleanupOnce.Do(func() {
			_ = input.Content.Close()
			if cause == nil {
				_ = pipeReader.Close()
			} else {
				_ = pipeReader.CloseWithError(cause)
			}
		})
	}
	writer := multipart.NewWriter(pipeWriter)
	contentType := writer.FormDataContentType()
	uploadDone := make(chan error, 1)
	go func() {
		err := writeUploadMultipart(writer, input)
		if err == nil {
			err = writer.Close()
		}
		if err == nil {
			_ = pipeWriter.Close()
		} else {
			_ = pipeWriter.CloseWithError(err)
		}
		uploadDone <- err
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/upload/image", pipeReader)
	if err != nil {
		cleanup(err)
		<-uploadDone
		return result, errors.New("comfyui upload request is invalid")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", contentType)
	stopCancellationWatch := make(chan struct{})
	cancellationWatchDone := make(chan struct{})
	go func() {
		defer close(cancellationWatchDone)
		select {
		case <-ctx.Done():
			cleanup(ctx.Err())
		case <-stopCancellationWatch:
		}
	}()
	response, requestErr := client.client.Do(request)
	close(stopCancellationWatch)
	cleanup(requestErr)
	<-cancellationWatchDone
	uploadErr := <-uploadDone
	if errors.Is(uploadErr, ErrUploadTooLarge) || errors.Is(requestErr, ErrUploadTooLarge) {
		if response != nil {
			response.Body.Close()
		}
		return result, ErrUploadTooLarge
	}
	if requestErr != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("comfyui image upload request failed")
	}
	defer response.Body.Close()
	if uploadErr != nil {
		return result, errors.New("comfyui image upload failed")
	}
	if err := checkStatus(response, "image upload"); err != nil {
		return result, err
	}
	if err := decodeJSONBody(response.Body, &result, "image upload"); err != nil {
		return result, err
	}
	if strings.TrimSpace(result.Name) == "" {
		return result, errors.New("comfyui image upload response is incomplete")
	}
	return result, nil
}

func writeUploadMultipart(writer *multipart.Writer, input UploadImageRequest) error {
	part, err := writer.CreateFormFile("image", input.Filename)
	if err != nil {
		return errors.New("comfyui image upload failed")
	}
	written, err := io.Copy(part, io.LimitReader(input.Content, maxUploadImageBytes+1))
	if written > maxUploadImageBytes {
		return ErrUploadTooLarge
	}
	if err != nil {
		return errors.New("comfyui image upload failed")
	}
	if err := writer.WriteField("type", input.Type); err != nil {
		return errors.New("comfyui image upload failed")
	}
	if err := writer.WriteField("subfolder", input.Subfolder); err != nil {
		return errors.New("comfyui image upload failed")
	}
	if err := writer.WriteField("overwrite", strconv.FormatBool(input.Overwrite)); err != nil {
		return errors.New("comfyui image upload failed")
	}
	return nil
}

func (client *httpClient) SubmitPrompt(ctx context.Context, prompt json.RawMessage, clientID string) (PromptSubmission, error) {
	var result PromptSubmission
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	if len(prompt) == 0 || len(prompt) > maxPromptJSONBytes || !json.Valid(prompt) || firstJSONByte(prompt) != '{' {
		return result, errors.New("comfyui prompt must be a bounded JSON object")
	}
	if strings.TrimSpace(clientID) == "" || len(clientID) > maxIdentifierBytes {
		return result, errors.New("comfyui client ID is invalid")
	}
	payload, err := json.Marshal(struct {
		Prompt   json.RawMessage `json:"prompt"`
		ClientID string          `json:"client_id"`
	}{Prompt: prompt, ClientID: clientID})
	if err != nil || len(payload) > maxPromptJSONBytes+maxIdentifierBytes {
		return result, errors.New("comfyui prompt request is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/prompt", bytes.NewReader(payload))
	if err != nil {
		return result, errors.New("comfyui prompt request is invalid")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, err := client.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return result, errors.Join(ErrSubmissionOutcomeUnknown, ctx.Err())
		}
		return result, ErrSubmissionOutcomeUnknown
	}
	defer response.Body.Close()
	if err := classifyPromptSubmissionStatus(response); err != nil {
		return result, err
	}
	if err := decodeJSONBody(response.Body, &result, "prompt submission"); err != nil {
		return result, errors.Join(ErrSubmissionOutcomeUnknown, err)
	}
	if strings.TrimSpace(result.PromptID) == "" {
		return result, errors.Join(ErrSubmissionOutcomeUnknown, errors.New("comfyui prompt submission response is incomplete"))
	}
	return result, nil
}

func classifyPromptSubmissionStatus(response *http.Response) error {
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	statusErr := &HTTPStatusError{Operation: "prompt submission", StatusCode: response.StatusCode}
	payload, err := readBounded(response.Body, maxErrorResponseBytes)
	if err != nil {
		return errors.Join(ErrSubmissionOutcomeUnknown, statusErr, err)
	}
	if response.StatusCode < http.StatusBadRequest || response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests {
		return errors.Join(ErrSubmissionOutcomeUnknown, statusErr)
	}
	if !json.Valid(payload) || firstJSONByte(payload) != '{' {
		return errors.Join(ErrSubmissionOutcomeUnknown, statusErr, errors.New("comfyui prompt submission returned malformed JSON"))
	}
	return statusErr
}

func firstJSONByte(raw json.RawMessage) byte {
	for _, character := range raw {
		switch character {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return character
		}
	}
	return 0
}

func (client *httpClient) History(ctx context.Context, promptID string) (PromptHistory, error) {
	var result PromptHistory
	if err := validateIdentifier(promptID, "prompt ID"); err != nil {
		return result, err
	}
	var response map[string]PromptHistory
	if err := client.getJSON(ctx, "/history/"+url.PathEscape(promptID), "prompt history", &response); err != nil {
		return result, err
	}
	result, ok := response[promptID]
	if !ok {
		return PromptHistory{}, errors.New("comfyui history did not include requested prompt")
	}
	result.PromptID = promptID
	return result, nil
}

func (client *httpClient) View(ctx context.Context, output OutputFile) (io.ReadCloser, http.Header, error) {
	if err := requireContext(ctx); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(output.Filename) == "" || len(output.Filename) > maxIdentifierBytes || len(output.Subfolder) > maxIdentifierBytes || len(output.Type) > maxIdentifierBytes {
		return nil, nil, errors.New("comfyui output file is invalid")
	}
	query := url.Values{}
	query.Set("filename", output.Filename)
	query.Set("subfolder", output.Subfolder)
	query.Set("type", output.Type)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/view?"+query.Encode(), nil)
	if err != nil {
		return nil, nil, errors.New("comfyui view request is invalid")
	}
	response, err := client.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("comfyui view request failed")
	}
	if err := checkStatus(response, "view"); err != nil {
		response.Body.Close()
		return nil, nil, err
	}
	return response.Body, response.Header.Clone(), nil
}

func (client *httpClient) DeleteQueuedPrompt(ctx context.Context, promptID string) (bool, error) {
	if err := validateIdentifier(promptID, "prompt ID"); err != nil {
		return false, err
	}
	payload, err := json.Marshal(struct {
		Delete []string `json:"delete"`
	}{Delete: []string{promptID}})
	if err != nil {
		return false, errors.New("comfyui queue deletion request is invalid")
	}
	request, err := client.newRequest(ctx, http.MethodPost, "/queue", bytes.NewReader(payload), "application/json")
	if err != nil {
		return false, err
	}
	response, err := client.do(request, "queue deletion")
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if err := checkStatus(response, "queue deletion"); err != nil {
		return false, err
	}
	if _, err := readBounded(response.Body, maxJSONResponseBytes); err != nil {
		return false, err
	}
	return true, nil
}

func (client *httpClient) getJSON(ctx context.Context, path string, operation string, target any) error {
	request, err := client.newRequest(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	response, err := client.do(request, operation)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := checkStatus(response, operation); err != nil {
		return err
	}
	return decodeJSONBody(response.Body, target, operation)
}

func (client *httpClient) newRequest(ctx context.Context, method string, path string, body io.Reader, contentType string) (*http.Request, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return nil, errors.New("comfyui request is invalid")
	}
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request, nil
}

func (client *httpClient) do(request *http.Request, operation string) (*http.Response, error) {
	response, err := client.client.Do(request)
	if err == nil {
		return response, nil
	}
	if request.Context().Err() != nil {
		return nil, request.Context().Err()
	}
	return nil, fmt.Errorf("comfyui %s request failed", operation)
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("comfyui request context is required")
	}
	return ctx.Err()
}

func validateIdentifier(value string, name string) error {
	if strings.TrimSpace(value) == "" || len(value) > maxIdentifierBytes || value == "." || value == ".." {
		return fmt.Errorf("comfyui %s is invalid", name)
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return fmt.Errorf("comfyui %s is invalid", name)
	}
	return nil
}

func checkStatus(response *http.Response, operation string) error {
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if _, err := readBounded(response.Body, maxErrorResponseBytes); err != nil {
		return err
	}
	return &HTTPStatusError{Operation: operation, StatusCode: response.StatusCode}
}

func decodeJSONBody(body io.Reader, target any, operation string) error {
	payload, err := readBounded(body, maxJSONResponseBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("comfyui %s returned malformed JSON", operation)
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		stable := errors.New("comfyui response body could not be read")
		if errors.Is(err, context.Canceled) {
			return nil, errors.Join(stable, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.Join(stable, context.DeadlineExceeded)
		}
		return nil, stable
	}
	if int64(len(payload)) > limit {
		return nil, ErrResponseTooLarge
	}
	return payload, nil
}

func (item *QueueItem) UnmarshalJSON(raw []byte) error {
	var fields []json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) < 2 {
		return errors.New("invalid queue item")
	}
	if err := json.Unmarshal(fields[0], &item.Number); err != nil {
		return errors.New("invalid queue item number")
	}
	if err := json.Unmarshal(fields[1], &item.PromptID); err != nil || strings.TrimSpace(item.PromptID) == "" {
		return errors.New("invalid queue prompt ID")
	}
	if len(fields) > 2 {
		item.Prompt = append(item.Prompt[:0], fields[2]...)
	}
	if len(fields) > 3 {
		item.ExtraData = append(item.ExtraData[:0], fields[3]...)
	}
	if len(fields) > 4 {
		item.OutputsToExecute = append(item.OutputsToExecute[:0], fields[4]...)
	}
	return nil
}
