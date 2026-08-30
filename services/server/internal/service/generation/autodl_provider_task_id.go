package generation

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

func encodeAutoDLZImageProviderTaskID(instanceProfileID string, promptID string) (string, error) {
	instanceProfileID = strings.TrimSpace(instanceProfileID)
	promptID = strings.TrimSpace(promptID)
	if instanceProfileID == "" || promptID == "" {
		return "", fmt.Errorf("AutoDL Z-Image provider task ID requires instance and prompt IDs")
	}
	return strings.Join([]string{
		coregeneration.RouteAutoDLZImage,
		base64.RawURLEncoding.EncodeToString([]byte(instanceProfileID)),
		base64.RawURLEncoding.EncodeToString([]byte(promptID)),
	}, ":"), nil
}

func parseAutoDLZImageProviderTaskID(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 || parts[0] != coregeneration.RouteAutoDLZImage || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("invalid AutoDL Z-Image provider task ID")
	}
	instanceProfileID, err := decodeAutoDLZImageProviderTaskIDSegment(parts[1])
	if err != nil {
		return "", "", err
	}
	promptID, err := decodeAutoDLZImageProviderTaskIDSegment(parts[2])
	if err != nil {
		return "", "", err
	}
	return instanceProfileID, promptID, nil
}

func decodeAutoDLZImageProviderTaskIDSegment(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || !utf8.Valid(decoded) || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return "", fmt.Errorf("invalid AutoDL Z-Image provider task ID encoding")
	}
	return string(decoded), nil
}

func validateAutoDLProviderCheckpoint(routeID string, state GenerationTaskRuntimeState, providerTaskID string) error {
	if err := validateCompleteAutoDLPromptCheckpoint(state); err != nil {
		return err
	}
	switch strings.TrimSpace(routeID) {
	case coregeneration.RouteAutoDLZImage:
		instanceProfileID, promptID, err := parseAutoDLZImageProviderTaskID(providerTaskID)
		if err != nil {
			return err
		}
		if instanceProfileID != strings.TrimSpace(state.InstanceProfileID) || promptID != strings.TrimSpace(state.ComfyPromptID) {
			return fmt.Errorf("AutoDL Z-Image provider task ID does not match its runtime checkpoint")
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
