package generation

import (
	"fmt"
	"strings"
)

const (
	maxPromptOptimizationContextDocumentCount = 64
	maxPromptOptimizationDocumentContextBytes = 120 << 10
)

func (workflow *GenerationService) promptOptimizationContextDocuments(
	payload *generationMessageRequest,
) ([]promptOptimizationContextDocument, error) {
	if payload == nil {
		return nil, nil
	}
	documentID := strings.TrimSpace(payload.DocumentID)
	if documentID == "" && payload.DocumentContext != nil {
		documentID = strings.TrimSpace(payload.DocumentContext.DocumentID)
	}
	if documentID == "" {
		return nil, nil
	}
	if workflow == nil || workflow.documents == nil {
		return nil, fmt.Errorf("提示词优化无法读取项目文档")
	}
	projectID := GenerationProjectIDForRequest(payload.ProjectID, "")
	current, err := workflow.documents.RequireWorkspaceDocument(projectID, documentID)
	if err != nil {
		return nil, fmt.Errorf("读取提示词优化来源文档 %q: %w", documentID, err)
	}

	documents := make([]promptOptimizationContextDocument, 0, 8)
	seen := map[string]struct{}{}
	totalBytes := 0
	appendDocument := func(kind string, linkedDocumentID string, sectionID string, title string, category string, markdown string) error {
		key := strings.TrimSpace(linkedDocumentID) + "\x00" + strings.TrimSpace(sectionID)
		if _, ok := seen[key]; ok {
			return nil
		}
		content := promptOptimizationDocumentContextText(markdown)
		if content == "" {
			return nil
		}
		if len(documents) >= maxPromptOptimizationContextDocumentCount {
			return fmt.Errorf("提示词优化上下文文档数量超过安全限制")
		}
		totalBytes += len(content)
		if totalBytes > maxPromptOptimizationDocumentContextBytes {
			return fmt.Errorf("提示词优化文档上下文超过安全限制")
		}
		seen[key] = struct{}{}
		documents = append(documents, promptOptimizationContextDocument{
			Index:      len(documents) + 1,
			Kind:       kind,
			Title:      strings.TrimSpace(title),
			Category:   strings.TrimSpace(category),
			DocumentID: strings.TrimSpace(linkedDocumentID),
			SectionID:  strings.TrimSpace(sectionID),
			Content:    content,
		})
		return nil
	}

	if err := appendDocument("current_document", current.ID, "", current.Title, current.Category, current.Content); err != nil {
		return nil, err
	}
	mentionSource := current.Content
	if prompt := strings.TrimSpace(payload.Prompt); prompt != "" {
		mentionSource += "\n\n" + prompt
	}
	for _, reference := range generationMentionsFromMarkdown(mentionSource) {
		if reference.Kind == "asset" || reference.DocumentID == "" || reference.DocumentID == current.ID {
			continue
		}
		document, err := workflow.documents.RequireWorkspaceDocument(projectID, reference.DocumentID)
		if err != nil {
			return nil, fmt.Errorf("读取提示词优化关联文档 %q: %w", reference.DocumentID, err)
		}
		markdown := document.Content
		sectionID := ""
		kind := "linked_document"
		if reference.Kind == "section" {
			section, ok := generationDocumentSectionByBlockID(document, reference.BlockID)
			if !ok {
				return nil, fmt.Errorf("提示词优化关联章节不存在：%s/%s", reference.DocumentID, reference.BlockID)
			}
			markdown = section.Markdown
			sectionID = reference.BlockID
			kind = "linked_section"
		}
		if err := appendDocument(kind, document.ID, sectionID, document.Title, document.Category, markdown); err != nil {
			return nil, err
		}
	}
	return documents, nil
}

func promptOptimizationDocumentContextText(markdown string) string {
	text := stripGenerationDocumentImageLines(stripGenerationSectionIDCommentLines(markdown))
	text = generationDocumentMentionPattern.ReplaceAllStringFunc(text, func(value string) string {
		match := generationDocumentMentionPattern.FindStringSubmatch(value)
		if len(match) < 2 {
			return ""
		}
		return generationUnescapeMentionLabel(match[1])
	})
	return strings.TrimSpace(text)
}
