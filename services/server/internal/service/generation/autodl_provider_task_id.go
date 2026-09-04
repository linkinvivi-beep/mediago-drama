package generation

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

func encodeAutoDLImageProviderTaskID(instanceProfileID string, promptID string) (string, error) {
	instanceProfileID = strings.TrimSpace(instanceProfileID)
	promptID = strings.TrimSpace(promptID)
	if instanceProfileID == "" || promptID == "" {
		return "", fmt.Errorf("AutoDL image provider task ID requires instance and prompt IDs")
	}
	return strings.Join([]string{
		coregeneration.RouteAutoDLImage,
		base64.RawURLEncoding.EncodeToString([]byte(instanceProfileID)),
		base64.RawURLEncoding.EncodeToString([]byte(promptID)),
	}, ":"), nil
}

func parseAutoDLImageProviderTaskID(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 || parts[0] != coregeneration.RouteAutoDLImage || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("invalid AutoDL image provider task ID")
	}
	instanceProfileID, err := decodeAutoDLImageProviderTaskIDSegment(parts[1])
	if err != nil {
		return "", "", err
	}
	promptID, err := decodeAutoDLImageProviderTaskIDSegment(parts[2])
	if err != nil {
		return "", "", err
	}
	return instanceProfileID, promptID, nil
}

func decodeAutoDLImageProviderTaskIDSegment(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || !utf8.Valid(decoded) || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", fmt.Errorf("invalid AutoDL image provider task ID encoding")
	}
	return string(decoded), nil
}

func validateAutoDLProviderCheckpoint(routeID string, state GenerationTaskRuntimeState, providerTaskID string) error {
	if err := validateCompleteAutoDLPromptCheckpoint(state); err != nil {
		return err
	}
	switch strings.TrimSpace(routeID) {
	case coregeneration.RouteAutoDLImage:
		instanceProfileID, promptID, err := parseAutoDLImageProviderTaskID(providerTaskID)
		if err != nil {
			return err
		}
		if instanceProfileID != strings.TrimSpace(state.InstanceProfileID) || promptID != strings.TrimSpace(state.ComfyPromptID) {
			return fmt.Errorf("AutoDL image provider task ID does not match its runtime checkpoint")
		}
		return nil
	case coregeneration.RouteAutoDLH3:
		parts := strings.Split(strings.TrimSpace(providerTaskID), ":")
		if len(parts) != 2 || parts[0] != coregeneration.RouteAutoDLH3 || parts[1] == "" || parts[1] != strings.TrimSpace(state.ComfyPromptID) {
			return fmt.Errorf("AutoDL H3 provider task ID does not match its prompt checkpoint")
		}
		return nil
	default:
		return fmt.Errorf("unsupported AutoDL provider task ID route")
	}
}
