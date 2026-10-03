package thread

import (
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
	json "encoding/json"
	fmt "fmt"
	schema "github.com/cloudwego/eino/schema"
	strconv "strconv"
	strings "strings"
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

	einoMessageAttributeExtraKey = "__cloudagent_message_attribute__"
	legacyMessageIDExtraKey      = "message_id"
)

type MessageAttribute struct {
	MessageID  string `json:"message_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderType string `json:"sender_type,omitempty"`
}

func init() {
	// MessageAttribute is stored in schema.Message.Extra. Eino checkpoint
	// serialization needs a stable name to restore custom Extra values.
	// Keep the historical names so existing local checkpoints remain readable.
	schema.RegisterName[MessageAttribute]("_cloudagent_message_attribute")
	schema.RegisterName[protocolInputParts]("_cloudagent_input_parts")
	schema.RegisterName[inputpkg.MessagePart]("_cloudagent_input_message_part")
}

func attributeFromWorkerMessage(message *TransportMessage) MessageAttribute {
	if message == nil {
		return MessageAttribute{}
	}
	attr := MessageAttribute{MessageID: strings.TrimSpace(message.ID)}
	if message.Sender != nil {
		attr.SenderID = strings.TrimSpace(message.Sender.ID)
		attr.SenderType = strings.TrimSpace(string(message.Sender.Type))
	}
	return attr
}

func attachAttribute(msg *schema.Message, attr MessageAttribute) {
	if msg == nil || attr.empty() {
		return
	}
	if msg.Extra == nil {
		msg.Extra = make(map[string]any, 1)
	}
	msg.Extra[einoMessageAttributeExtraKey] = attr
}

func attributeFromMessage(msg *schema.Message) MessageAttribute {
	if msg == nil || msg.Extra == nil {
		return MessageAttribute{}
	}
	attr, ok := attributeFromExtraValue(msg.Extra[einoMessageAttributeExtraKey])
	if ok {
		return attr
	}
	messageID, stringIDFromAnyOK := stringIDFromAny(msg.Extra[legacyMessageIDExtraKey])
	if stringIDFromAnyOK {
		return MessageAttribute{MessageID: messageID}
	}
	return MessageAttribute{}
}

func ConsumedMessageIDs(inputs []*schema.Message) []string {
	if len(inputs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		attr := attributeFromMessage(input)
		if attr.MessageID != "" {
			ids = append(ids, attr.MessageID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// MessageID returns the Manager mailbox identity attached to a user message.
// It is used by durable history storage to make Worker redelivery idempotent.
func MessageID(message *schema.Message) string {
	return attributeFromMessage(message).MessageID
}

func attributeFromExtraValue(raw any) (MessageAttribute, bool) {
	switch v := raw.(type) {
	case MessageAttribute:
		attr := v.normalized()
		return attr, !attr.empty()
	case *MessageAttribute:
		if v == nil {
			return MessageAttribute{}, false
		}
		attr := v.normalized()
		return attr, !attr.empty()
	case map[string]any:
		attr := MessageAttribute{}
		messageID, ok := stringIDFromAny(v["message_id"])
		if ok {
			attr.MessageID = messageID
		}
		senderID, stringIDFromAnyOK2 := stringIDFromAny(v["sender_id"])
		if stringIDFromAnyOK2 {
			attr.SenderID = senderID
		}
		senderType, stringIDFromAnyOK := stringIDFromAny(v["sender_type"])
		if stringIDFromAnyOK {
			attr.SenderType = senderType
		}
		attr = attr.normalized()
		return attr, !attr.empty()
	default:
		return MessageAttribute{}, false
	}
}

func (a MessageAttribute) normalized() MessageAttribute {
	return MessageAttribute{
		MessageID:  strings.TrimSpace(a.MessageID),
		SenderID:   strings.TrimSpace(a.SenderID),
		SenderType: strings.TrimSpace(a.SenderType),
	}
}

func (a MessageAttribute) empty() bool {
	a = a.normalized()
	return a.MessageID == "" && a.SenderID == "" && a.SenderType == ""
}

func stringIDFromAny(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		id := strings.TrimSpace(v)
		return id, id != ""
	case int64:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(v, 10), true
	case int:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case float64:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case json.Number:
		n, err := v.Int64()
		if err == nil && n != 0 {
			return strconv.FormatInt(n, 10), true
		}
		return "", false
	default:
		return "", false
	}
}

const protocolInputPartsExtraKey = "__cloudagent_input_parts__"

type protocolInputParts []inputpkg.MessagePart

func protocolUserMessageToSchemaMessage(input inputpkg.UserMessage) (*schema.Message, error) {
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

	msg := &schema.Message{Role: schema.User, Extra: protocolExtraToSchemaExtra(input.Extra)}
	if hasNonTextPart {
		msg.UserInputMultiContent = parts
	} else {
		msg.Content = strings.Join(text, "\n")
	}
	attachOriginalProtocolInputParts(msg, originalParts)
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

func schemaUserMessageToProtocolParts(message *schema.Message) []eventpkg.MessagePart {
	originalProtocolInputPartsParts := originalProtocolInputParts(message)
	if len(originalProtocolInputPartsParts) > 0 {
		return inputPartsForEvent(originalProtocolInputPartsParts)
	}
	if message == nil {
		return nil
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

func schemaAssistantMessageToProtocolParts(message *schema.Message) []eventpkg.MessagePart {
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

func attachOriginalProtocolInputParts(msg *schema.Message, parts []inputpkg.MessagePart) {
	if msg == nil || len(parts) == 0 {
		return
	}
	if msg.Extra == nil {
		msg.Extra = make(map[string]any, 1)
	}
	msg.Extra[protocolInputPartsExtraKey] = protocolInputParts(cloneProtocolInputParts(parts))
}

func originalProtocolInputParts(msg *schema.Message) []inputpkg.MessagePart {
	if msg == nil || msg.Extra == nil {
		return nil
	}
	switch parts := msg.Extra[protocolInputPartsExtraKey].(type) {
	case protocolInputParts:
		return cloneProtocolInputParts([]inputpkg.MessagePart(parts))
	case *protocolInputParts:
		if parts == nil {
			return nil
		}
		return cloneProtocolInputParts([]inputpkg.MessagePart(*parts))
	case []inputpkg.MessagePart:
		return cloneProtocolInputParts(parts)
	default:
		return nil
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
