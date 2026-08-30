package generation

import (
	"encoding/json"
	"strconv"
	"strings"
)

const generationOrderedReferencesParam = "_mediago_ordered_references"

// generationOrderedReference is the canonical server-owned reference slot.
// Source is an opaque stable key (url:... or asset:...), never provider data.
type generationOrderedReference struct {
	Index  int    `json:"index"`
	Label  string `json:"label"`
	Role   string `json:"role"`
	Source string `json:"source"`
}

func canonicalOrderedGenerationReferences(payload generationMessageRequest) []generationOrderedReference {
	roles := map[string]string{}
	for _, binding := range generationReferenceBindingsForPayload(payload) {
		source := generationReferenceBindingSourceKey(binding)
		if source == "" || roles[source] != "" {
			continue
		}
		roles[source] = generationReferenceBindingMentionKey(binding)
	}

	ordered := make([]generationOrderedReference, 0, len(payload.ReferenceURLs)+len(payload.ReferenceAssetIDs))
	seen := map[string]struct{}{}
	appendSource := func(source string) {
		source = strings.TrimSpace(source)
		if source == "" {
			return
		}
		if _, exists := seen[source]; exists {
			return
		}
		seen[source] = struct{}{}
		role := strings.TrimSpace(roles[source])
		if role == "" {
			role = "reference"
		}
		ordered = append(ordered, generationOrderedReference{
			Index:  len(ordered) + 1,
			Label:  "参考图" + strconv.Itoa(len(ordered)+1),
			Role:   role,
			Source: source,
		})
	}

	for _, value := range CompactStrings(payload.ReferenceURLs) {
		if assetID := libraryAssetIDFromGenerationAssetURL(value); assetID != "" {
			appendSource("asset:" + assetID)
		} else {
			appendSource("url:" + value)
		}
	}
	for _, assetID := range CompactStrings(payload.ReferenceAssetIDs) {
		appendSource("asset:" + assetID)
	}
	return ordered
}

func generationParamsWithOrderedReferences(params map[string]any, ordered []generationOrderedReference) map[string]any {
	next := make(map[string]any, len(params)+1)
	for key, value := range params {
		if key != generationOrderedReferencesParam {
			next[key] = value
		}
	}
	if len(ordered) > 0 {
		next[generationOrderedReferencesParam] = ordered
	}
	return next
}

func orderedGenerationReferencesFromParams(params map[string]any) []generationOrderedReference {
	raw, ok := params[generationOrderedReferencesParam]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded []generationOrderedReference
	if json.Unmarshal(data, &decoded) != nil {
		return nil
	}
	ordered := make([]generationOrderedReference, 0, len(decoded))
	seen := map[string]struct{}{}
	for _, item := range decoded {
		item.Source = strings.TrimSpace(item.Source)
		if item.Source == "" || (!strings.HasPrefix(item.Source, "url:") && !strings.HasPrefix(item.Source, "asset:")) {
			return nil
		}
		if _, exists := seen[item.Source]; exists {
			return nil
		}
		seen[item.Source] = struct{}{}
		item.Index = len(ordered) + 1
		item.Label = "参考图" + strconv.Itoa(item.Index)
		item.Role = strings.TrimSpace(item.Role)
		if item.Role == "" {
			item.Role = "reference"
		}
		ordered = append(ordered, item)
	}
	return ordered
}
