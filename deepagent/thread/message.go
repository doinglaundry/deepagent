package thread

import (
	json "encoding/json"
	fmt "fmt"
	strings "strings"

	messagepkg "eino-cli/deepagent/message"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"

	schema "github.com/cloudwego/eino/schema"
)

func parseUserMessage(message *TransportMessage) (inputpkg.UserMessage, error) {
	if message == nil {
		return inputpkg.UserMessage{}, fmt.Errorf("message is required")
	}
	var input inputpkg.UserMessage
	err := json.Unmarshal(message.Payload, &input)
	if err != nil {
		return inputpkg.UserMessage{}, fmt.Errorf("unmarshal user message: %w", err)
	}
	validationErr := input.Validate()
	if validationErr != nil {
		return inputpkg.UserMessage{}, validationErr
	}
	return input, nil
}

const (
	MessageTypeInput     TransportMessageType = TransportMessageType(inputpkg.MessageTypeInput)
	MessageTypeResumeRun TransportMessageType = TransportMessageType(inputpkg.MessageTypeResume)
	MessageTypeCompact   TransportMessageType = TransportMessageType(inputpkg.MessageTypeCompact)

	MetadataRunMode = inputpkg.MetadataRunMode
	RunModePlan     = inputpkg.RunModePlan
)

func ConsumedMessageIDs(inputs []*messagepkg.Message) []string {
	var ids []string
	for _, input := range inputs {
		if input != nil && input.MessageID != "" {
			ids = append(ids, input.MessageID)
		}
	}
	return ids
}

func decodeDialogueMessage(input inputpkg.UserMessage) (*messagepkg.Message, error) {
	originalParts := normalizeProtocolInputParts(input.Parts)
	parts := make([]schema.MessageInputPart, 0, len(originalParts))
	text := make([]string, 0, len(originalParts))
	hasNonTextPart := false
	for i, part := range originalParts {
		einoPart, err := protocolPartToSchemaInputPart(part)
		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}
		parts = append(parts, einoPart)
		if part.Type == inputpkg.MessagePartTypeText {
			text = append(text, part.Text)
		} else {
			hasNonTextPart = true
		}
	}

	msg := &messagepkg.Message{Role: schema.User, Extra: protocolExtraToSchemaExtra(input.Extra)}
	if hasNonTextPart {
		msg.UserInputMultiContent = parts
	} else {
		msg.Content = strings.Join(text, "\n")
	}
	msg.OriginalParts = cloneProtocolInputParts(originalParts)
	return msg, nil
}

func protocolPartToSchemaInputPart(part inputpkg.MessagePart) (schema.MessageInputPart, error) {
	switch part.Type {
	case inputpkg.MessagePartTypeText:
		return schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeText,
			Text:  strings.TrimSpace(part.Text),
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeImage:
		return schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schemaMessagePartCommon(part), Detail: schema.ImageURLDetail(strings.TrimSpace(part.Detail))},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeAudio:
		return schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeAudioURL,
			Audio: &schema.MessageInputAudio{MessagePartCommon: schemaMessagePartCommon(part)},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeVideo:
		return schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeVideoURL,
			Video: &schema.MessageInputVideo{MessagePartCommon: schemaMessagePartCommon(part)},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeFile:
		return schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeFileURL,
			File:  &schema.MessageInputFile{MessagePartCommon: schemaMessagePartCommon(part), Name: strings.TrimSpace(part.Name)},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	default:
		return schema.MessageInputPart{}, fmt.Errorf("unsupported part type: %s", part.Type)
	}
}

func getUserMessageParts(message *messagepkg.Message) []eventpkg.MessagePart {
	if message == nil {
		return nil
	}
	if len(message.OriginalParts) > 0 {
		return inputPartsForEvent(message.OriginalParts)
	}
	if len(message.UserInputMultiContent) == 0 {
		return textParts(message.Content)
	}
	parts := make([]eventpkg.MessagePart, 0, len(message.UserInputMultiContent))
	hasText := false
	for _, part := range message.UserInputMultiContent {
		converted, ok := schemaInputPartToProtocolPart(part)
		if ok {
			parts = append(parts, converted)
			hasText = hasText || converted.Type == eventpkg.MessagePartTypeText
		}
	}
	if !hasText && message.Content != "" {
		parts = append(textParts(message.Content), parts...)
	}
	return parts
}

func schemaInputPartToProtocolPart(part schema.MessageInputPart) (eventpkg.MessagePart, bool) {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		text := strings.TrimSpace(part.Text)
		if text == "" {
			return eventpkg.MessagePart{}, false
		}
		return eventpkg.MessagePart{Type: eventpkg.MessagePartTypeText, Text: text, Extra: schemaExtraToProtocolExtra(part.Extra)}, true
	case schema.ChatMessagePartTypeImageURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeImage, messageInputImageCommon(part.Image))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		if part.Image != nil {
			out.Detail = string(part.Image.Detail)
		}
		return out, true
	case schema.ChatMessagePartTypeAudioURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeAudio, messageInputAudioCommon(part.Audio))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schema.ChatMessagePartTypeVideoURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeVideo, messageInputVideoCommon(part.Video))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schema.ChatMessagePartTypeFileURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeFile, messageInputFileCommon(part.File))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		if part.File != nil {
			out.Name = part.File.Name
		}
		return out, true
	default:
		return eventpkg.MessagePart{}, false
	}
}

func getAssistantMessageParts(message *messagepkg.Message) []eventpkg.MessagePart {
	if message == nil {
		return nil
	}
	if len(message.AssistantGenMultiContent) > 0 {
		parts := make([]eventpkg.MessagePart, 0, len(message.AssistantGenMultiContent))
		for _, part := range message.AssistantGenMultiContent {
			converted, ok := schemaOutputPartToProtocolPart(part)
			if ok {
				parts = append(parts, converted)
			}
		}
		if len(parts) > 0 {
			return parts
		}
	}
	return textParts(message.Content)
}

func schemaOutputPartToProtocolPart(part schema.MessageOutputPart) (eventpkg.MessagePart, bool) {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return eventpkg.MessagePart{Type: eventpkg.MessagePartTypeText, Text: part.Text, Extra: schemaExtraToProtocolExtra(part.Extra)}, true
	case schema.ChatMessagePartTypeImageURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeImage, messageOutputImageCommon(part.Image))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schema.ChatMessagePartTypeAudioURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeAudio, messageOutputAudioCommon(part.Audio))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schema.ChatMessagePartTypeVideoURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeVideo, messageOutputVideoCommon(part.Video))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	default:
		return eventpkg.MessagePart{Type: eventpkg.MessagePartType(strings.TrimSuffix(string(part.Type), "_url")), Extra: schemaExtraToProtocolExtra(part.Extra)}, true
	}
}

func inputPartsForEvent(parts []inputpkg.MessagePart) []eventpkg.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]eventpkg.MessagePart, 0, len(parts))
	for _, part := range parts {
		cloned := cloneProtocolInputPart(part)
		out = append(out, eventpkg.MessagePart{
			Type: eventpkg.MessagePartType(cloned.Type), Text: cloned.Text,
			URL: cloned.URL, MIMEType: cloned.MIMEType, Base64Data: cloned.Base64Data,
			Detail: cloned.Detail, Name: cloned.Name, Extra: cloned.Extra,
		})
	}
	return out
}

func normalizeProtocolInputParts(parts []inputpkg.MessagePart) []inputpkg.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]inputpkg.MessagePart, len(parts))
	for i, part := range parts {
		out[i] = inputpkg.MessagePart{
			Type:       part.Type,
			Text:       strings.TrimSpace(part.Text),
			URL:        strings.TrimSpace(part.URL),
			Base64Data: strings.TrimSpace(part.Base64Data),
			MIMEType:   strings.TrimSpace(part.MIMEType),
			Name:       strings.TrimSpace(part.Name),
			Detail:     strings.TrimSpace(part.Detail),
			Extra:      cloneProtocolExtra(part.Extra),
		}
	}
	return out
}

func cloneProtocolInputParts(parts []inputpkg.MessagePart) []inputpkg.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]inputpkg.MessagePart, len(parts))
	for i, part := range parts {
		out[i] = cloneProtocolInputPart(part)
	}
	return out
}

func schemaMessagePartCommon(part inputpkg.MessagePart) schema.MessagePartCommon {
	common := schema.MessagePartCommon{
		MIMEType: strings.TrimSpace(part.MIMEType),
		Extra:    protocolExtraToSchemaExtra(part.Extra),
	}
	url := strings.TrimSpace(part.URL)
	if url != "" {
		common.URL = &url
	}
	data := strings.TrimSpace(part.Base64Data)
	if data != "" {
		common.Base64Data = &data
	}
	return common
}

func protocolPartFromCommon(partType eventpkg.MessagePartType, common schema.MessagePartCommon) eventpkg.MessagePart {
	out := eventpkg.MessagePart{Type: partType, MIMEType: common.MIMEType, Extra: schemaExtraToProtocolExtra(common.Extra)}
	if common.URL != nil {
		out.URL = *common.URL
	}
	if common.Base64Data != nil {
		out.Base64Data = *common.Base64Data
	}
	return out
}

func cloneProtocolInputPart(part inputpkg.MessagePart) inputpkg.MessagePart {
	return inputpkg.MessagePart{
		Type:       part.Type,
		Text:       part.Text,
		URL:        part.URL,
		Base64Data: part.Base64Data,
		MIMEType:   part.MIMEType,
		Name:       part.Name,
		Detail:     part.Detail,
		Extra:      cloneProtocolExtra(part.Extra),
	}
}

func cloneProtocolExtra(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = cloneRawMessage(v)
	}
	return out
}

func protocolExtraToSchemaExtra(in map[string]json.RawMessage) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneRawMessage(v)
	}
	return out
}

func schemaExtraToProtocolExtra(in map[string]any) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		raw, ok := rawMessageFromAny(v)
		if ok {
			out[k] = raw
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func rawMessageFromAny(v any) (json.RawMessage, bool) {
	switch raw := v.(type) {
	case json.RawMessage:
		return cloneRawMessage(raw), true
	case *json.RawMessage:
		if raw == nil {
			return nil, false
		}
		return cloneRawMessage(*raw), true
	case []byte:
		if !json.Valid(raw) {
			break
		}
		return cloneRawMessage(json.RawMessage(raw)), true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

func mergeProtocolExtra(base map[string]json.RawMessage, overrides map[string]json.RawMessage) map[string]json.RawMessage {
	if len(base) == 0 {
		return cloneProtocolExtra(overrides)
	}
	out := cloneProtocolExtra(base)
	for k, v := range overrides {
		out[k] = cloneRawMessage(v)
	}
	return out
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage(nil), in...)
}

func messageInputImageCommon(part *schema.MessageInputImage) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageInputAudioCommon(part *schema.MessageInputAudio) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageInputVideoCommon(part *schema.MessageInputVideo) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageInputFileCommon(part *schema.MessageInputFile) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageOutputImageCommon(part *schema.MessageOutputImage) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageOutputAudioCommon(part *schema.MessageOutputAudio) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageOutputVideoCommon(part *schema.MessageOutputVideo) schema.MessagePartCommon {
	if part == nil {
		return schema.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func userInputMode(message *TransportMessage, mode inputpkg.UserMessageMode) inputpkg.UserMessageMode {
	if mode != "" {
		return mode
	}
	return messageMetadataMode(message)
}

func messageMetadataMode(message *TransportMessage) inputpkg.UserMessageMode {
	if message == nil || message.Metadata == nil {
		return ""
	}
	switch message.Metadata[MetadataRunMode] {
	case RunModePlan:
		return inputpkg.UserMessageModeImplPlan
	default:
		return ""
	}
}
